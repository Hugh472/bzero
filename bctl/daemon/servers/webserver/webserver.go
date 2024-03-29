package webserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	bzwebsocket "bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzweb "bastionzero.com/bctl/v1/bzerolib/plugin/web"
	"github.com/google/uuid"
	"gopkg.in/tomb.v2"
)

const (
	// websocket connection parameters for all datachannels created by tcp server
	autoReconnect = true
	getChallenge  = false

	// TODO: make these easily configurable values
	maxRequestSize = 10 * 1024 * 1024  // 10MB
	maxFileUpload  = 151 * 1024 * 1024 // 151MB a little extra for request fluff
)

type WebServer struct {
	logger    *logger.Logger
	websocket *bzwebsocket.Websocket
	tmb       tomb.Tomb

	// Handler to select message types
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// Web specific vars
	// Either user the full dns (i.e. targetHostName) or the host:port
	targetPort int
	targetHost string

	// fields for new datachannels
	localPort           string
	localHost           string
	params              map[string]string
	headers             map[string]string
	serviceUrl          string
	refreshTokenCommand string
	configPath          string
	agentPubKey         string
}

func StartWebServer(logger *logger.Logger,
	localPort string,
	localHost string,
	targetPort int,
	targetHost string,
	refreshTokenCommand string,
	configPath string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	listener := &WebServer{
		logger:              logger,
		serviceUrl:          serviceUrl,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		refreshTokenCommand: refreshTokenCommand,
		localPort:           localPort,
		localHost:           localHost,
		targetHost:          targetHost,
		targetPort:          targetPort,
		agentPubKey:         agentPubKey,
	}

	// Create a new websocket
	if err := listener.newWebsocket(uuid.New().String()); err != nil {
		listener.logger.Error(err)
		return err
	}

	// Create HTTP Server listens for incoming kubectl commands
	go func() {
		// Define our http handlers
		// library will automatically put each call in its own thread
		http.HandleFunc("/", listener.capRequestSize(listener.handleHttp))

		if err := http.ListenAndServe(fmt.Sprintf("%s:%s", localHost, localPort), nil); err != nil {
			logger.Error(err)
		}
	}()

	return nil
}

// this function operates as middleware between the http handler and the handleHttp call below
// it checks to see if someone is trying to send a request body that is far too large
func (w *WebServer) capRequestSize(h http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.Header.Get("Content-Type"), "multipart") {
			if request.ContentLength > maxFileUpload {
				// for multipart/form-data type requests, the request body won't exceed our maximum single request size
				// but we still want to cap the size of uploads because they are stored in their entirety on the target.
				// Not optimal, here's the ticket: CWC-1647
				// We shouldn't be relying on content length too much since it can be modified to be whatever.
				rerr := "BastionZero: Request is too large. Maximum upload is 150MB"
				w.logger.Errorf(rerr)
				http.Error(writer, rerr, http.StatusRequestEntityTooLarge)
				return
			}
		} else {
			request.Body = http.MaxBytesReader(writer, request.Body, maxRequestSize)
			if err := request.ParseForm(); err != nil {
				rerr := "BastionZero: Request is too large. Maximum request size is 10MB"
				w.logger.Errorf(rerr)
				http.Error(writer, rerr, http.StatusRequestEntityTooLarge)
				return
			}
		}

		h(writer, request)
	}
}

func (w *WebServer) handleHttp(writer http.ResponseWriter, request *http.Request) {
	action := bzweb.Dial

	// This will work for http 1.1 and that is what we need to support
	// Ref: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Upgrade
	// Ref: https://datatracker.ietf.org/doc/html/rfc6455#section-1.7
	isWebsocketRequest := request.Header.Get("Upgrade")
	if isWebsocketRequest == "websocket" {
		action = bzweb.Websocket
	}

	food := bzweb.WebFood{
		Action:  action,
		Request: request,
		Writer:  writer,
	}

	// create our new datachannel
	if dc, err := w.newDataChannel(string(action), w.websocket); err == nil {
		dc.Feed(food)
	} else {
		w.logger.Errorf("error starting datachannel: %s", err)
	}
}

// for creating new websockets
func (h *WebServer) newWebsocket(wsId string) error {
	subLogger := h.logger.GetWebsocketLogger(wsId)
	if wsClient, err := bzwebsocket.New(subLogger, h.serviceUrl, h.params, h.headers, h.targetSelectHandler, autoReconnect, getChallenge, h.refreshTokenCommand, bzwebsocket.Web); err != nil {
		return err
	} else {
		h.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (w *WebServer) newDataChannel(action string, websocket *bzwebsocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	subLogger := w.logger.GetDatachannelLogger(dcId)

	w.logger.Infof("Creating new datachannel for web with id: %v", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	actionParams := bzweb.WebActionParams{
		RemotePort: w.targetPort,
		RemoteHost: w.targetHost,
	}

	actionParamsMarshalled, marshalErr := json.Marshal(actionParams)
	if marshalErr != nil {
		w.logger.Error(fmt.Errorf("error marshalling action params for web"))
		return nil, marshalErr
	}

	action = "web/" + action
	if datachannel, dcTmb, err := datachannel.New(subLogger, dcId, &w.tmb, websocket, w.refreshTokenCommand, w.configPath, action, actionParamsMarshalled, w.agentPubKey); err != nil {
		w.logger.Error(err)
		return datachannel, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-w.tmb.Dying():
					datachannel.Close(errors.New("web server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// notify agent to close the datachannel
					w.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					w.websocket.Send(cdMessage)

					return
				}
			}
		}()
		return datachannel, nil
	}
}
