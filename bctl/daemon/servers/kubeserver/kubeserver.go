package kubeserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/tomb.v2"

	"bastionzero.com/bctl/v1/bctl/daemon/datachannel"
	"bastionzero.com/bctl/v1/bctl/daemon/plugin/kube"
	kubeutils "bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/utils"
	am "bastionzero.com/bctl/v1/bzerolib/channels/agentmessage"
	"bastionzero.com/bctl/v1/bzerolib/channels/websocket"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	bzkube "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
)

const (
	// This token is used when validating our Bearer token. Our token comes in with the form "{localhostToken}++++{english command i.e. zli kube get pods}++++{logId}"
	// The english command and logId are only generated if the user is using "zli kube ..."
	// So we use this securityTokenDelimiter to split up our token and extract what might be there
	securityTokenDelimiter = "++++"

	// websocket connection parameters for all datachannels created by http server
	autoReconnect = true
	getChallenge  = false
)

type StatusMessage struct {
	ExitMessage string `json:"ExitMessage"`
}

type KubeServer struct {
	logger      *logger.Logger
	websocket   *websocket.Websocket // TODO: This will need to be a dictionary for when we have multiple
	tmb         tomb.Tomb
	exitMessage string

	// RestApi is a special case where we want to be able to constantly retrieve it so we can feed any new RestApi
	// requests that come in and skip the overhead of asking for a new datachannel and sending a Syn
	restApiDatachannel *datachannel.DataChannel

	// fields for processing incoming kubectl commands
	localhostToken string

	// fields for opening websockets
	serviceUrl          string
	params              map[string]string
	headers             map[string]string
	targetSelectHandler func(msg am.AgentMessage) (string, error)

	// fields for new datachannels
	refreshTokenCommand string
	configPath          string
	targetUser          string
	targetGroups        []string
	agentPubKey         string
}

func StartKubeServer(logger *logger.Logger,
	localPort string,
	localHost string,
	certPath string,
	keyPath string,
	refreshTokenCommand string,
	configPath string,
	targetUser string,
	targetGroups []string,
	localhostToken string,
	serviceUrl string,
	params map[string]string,
	headers map[string]string,
	agentPubKey string,
	targetSelectHandler func(msg am.AgentMessage) (string, error)) error {

	listener := &KubeServer{
		logger:              logger,
		exitMessage:         "",
		localhostToken:      localhostToken,
		serviceUrl:          serviceUrl,
		params:              params,
		headers:             headers,
		targetSelectHandler: targetSelectHandler,
		configPath:          configPath,
		targetUser:          targetUser,
		targetGroups:        targetGroups,
		refreshTokenCommand: refreshTokenCommand,
		agentPubKey:         agentPubKey,
	}

	// Create a new websocket
	if err := listener.newWebsocket(uuid.New().String()); err != nil {
		listener.logger.Error(err)
		return err
	}

	// Create a single datachannel for all of our rest api calls to reduce overhead
	if datachannel, err := listener.newDataChannel(string(bzkube.RestApi), listener.websocket); err == nil {
		listener.restApiDatachannel = datachannel
	} else {
		return err
	}

	// Create HTTP Server listens for incoming kubectl commands
	go func() {
		// Define our http handlers
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			listener.rootCallback(logger, w, r)
		})

		http.HandleFunc("/bastionzero-ready", func(w http.ResponseWriter, r *http.Request) {
			listener.isReadyCallback(w, r)
		})

		http.HandleFunc("/bastionzero-status", func(w http.ResponseWriter, r *http.Request) {
			listener.statusCallback(w, r)
		})

		if err := http.ListenAndServeTLS(localHost+":"+localPort, certPath, keyPath, nil); err != nil {
			logger.Error(err)
		}
	}()

	return nil
}

func (k *KubeServer) isReadyCallback(w http.ResponseWriter, r *http.Request) {
	if k.restApiDatachannel.Ready() {
		w.WriteHeader(http.StatusOK)
		return
	} else {
		w.WriteHeader(http.StatusTooEarly)
		return
	}
}

func (k *KubeServer) statusCallback(w http.ResponseWriter, r *http.Request) {
	// Build our status message
	statusMessage := StatusMessage{
		ExitMessage: k.exitMessage,
	}

	registerJson, err := json.Marshal(statusMessage)
	if err != nil {
		k.logger.Error(fmt.Errorf("error marshalling status message: %+v", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(registerJson)
}

// for creating new websockets
func (h *KubeServer) newWebsocket(wsId string) error {
	subLogger := h.logger.GetWebsocketLogger(wsId)
	if wsClient, err := websocket.New(subLogger, h.serviceUrl, h.params, h.headers, h.targetSelectHandler, autoReconnect, getChallenge, h.refreshTokenCommand, websocket.Cluster); err != nil {
		return err
	} else {
		h.websocket = wsClient
		return nil
	}
}

// for creating new datachannels
func (h *KubeServer) newDataChannel(action string, websocket *websocket.Websocket) (*datachannel.DataChannel, error) {
	// every datachannel gets a uuid to distinguish it so a single websockets can map to multiple datachannels
	dcId := uuid.New().String()
	subLogger := h.logger.GetDatachannelLogger(dcId)

	h.logger.Infof("Creating new datachannel id: %v", dcId)

	// Build the actionParams to send to the datachannel to start the plugin
	actionParams := bzkube.KubeActionParams{
		TargetUser:   h.targetUser,
		TargetGroups: h.targetGroups,
	}

	actionParamsMarshalled, marshalErr := json.Marshal(actionParams)
	if marshalErr != nil {
		h.logger.Error(fmt.Errorf("error marshalling action params for kube"))
		return nil, marshalErr
	}

	action = "kube/" + action
	if datachannel, dcTmb, err := datachannel.New(subLogger, dcId, &h.tmb, websocket, h.refreshTokenCommand, h.configPath, action, actionParamsMarshalled, h.agentPubKey); err != nil {
		h.logger.Error(err)
		return datachannel, err
	} else {

		// create a function to listen to the datachannel dying and then laugh
		go func() {
			for {
				select {
				case <-h.tmb.Dying():
					datachannel.Close(errors.New("kube server closing"))
					return
				case <-dcTmb.Dying():
					// Wait until everything is dead and any close processes are sent before killing the datachannel
					dcTmb.Wait()

					// only report the error if it's not nil.  Otherwise,  we assume the datachannel closed legitimately.
					if err := dcTmb.Err(); err != nil {
						h.exitMessage = dcTmb.Err().Error()
					}

					// notify agent to close the datachannel
					h.logger.Info("Sending DataChannel Close")
					cdMessage := am.AgentMessage{
						ChannelId:   dcId,
						MessageType: string(am.CloseDataChannel),
					}
					h.websocket.Send(cdMessage)

					// close our websocket if the datachannel we closed was the last and it's not rest api
					if bzkube.KubeAction(action) != bzkube.RestApi && h.websocket.SubscriberCount() == 0 {
						h.websocket.Close(errors.New("all datachannels closed, closing websocket"))
					}
					return
				}
			}
		}()
		return datachannel, nil
	}
}

func (h *KubeServer) bubbleUpError(w http.ResponseWriter, msg string, statusCode int) {
	w.WriteHeader(statusCode)
	h.logger.Error(errors.New(msg))
	w.Write([]byte(msg))
}

func (h *KubeServer) rootCallback(logger *logger.Logger, w http.ResponseWriter, r *http.Request) {
	h.logger.Infof("Handling %s - %s\n", r.URL.Path, r.Method)

	// Before processing, check if we're ready to process or if there's been an error
	switch {
	case !h.restApiDatachannel.Ready():
		h.bubbleUpError(w, "Daemon starting up...", http.StatusTooEarly)
		return
	case h.exitMessage != "":
		msg := fmt.Sprintf("error on daemon: " + h.exitMessage)
		h.bubbleUpError(w, msg, http.StatusInternalServerError)
		return
	}

	// First verify our token and extract any commands if we can
	tokenToValidate := r.Header.Get("Authorization")

	// Remove the `Bearer `
	tokenToValidate = strings.Replace(tokenToValidate, "Bearer ", "", -1)

	// Validate the token
	tokensSplit := strings.Split(tokenToValidate, securityTokenDelimiter)
	if tokensSplit[0] != h.localhostToken {
		h.bubbleUpError(w, "localhost token did not validate. Ensure you are using the right Kube config file", http.StatusInternalServerError)
		return
	}

	// Check if we have a command to extract
	command := "N/A" // TODO: should be empty string
	logId := uuid.New().String()
	if len(tokensSplit) == 3 {
		command = tokensSplit[1]
		logId = tokensSplit[2]
	}

	// Determine the action
	action := getAction(r)

	// Make food
	food := kube.KubeFood{
		Action:  action,
		LogId:   logId,
		Command: command,
		Writer:  w,
		Reader:  r,
	}

	// feed our restapi datachannel
	if action == bzkube.RestApi {
		h.restApiDatachannel.Feed(food)

		// create new datachannel and feed it kubectl handlers
	} else if datachannel, err := h.newDataChannel(string(action), h.websocket); err == nil {
		datachannel.Feed(food)
	}
}

func getAction(req *http.Request) bzkube.KubeAction {
	// parse action from incoming request
	switch {
	// interactive commands that require both stdin and stdout
	case isExecRequest(req):
		return bzkube.Exec

	// Persistent, yet not interactive commands that serve continual output but only listen for a single, request-cancelling input
	case isPortForwardRequest(req):
		return bzkube.Stream
	case isStreamRequest(req):
		return bzkube.Stream

	// simple call and response aka restapi requests
	default:
		return bzkube.RestApi
	}
}

func isPortForwardRequest(request *http.Request) bool {
	return strings.HasSuffix(request.URL.Path, "/portforward")
}

func isExecRequest(request *http.Request) bool {
	return strings.HasSuffix(request.URL.Path, "/exec") || strings.HasSuffix(request.URL.Path, "/attach")
}

func isStreamRequest(request *http.Request) bool {
	return (strings.HasSuffix(request.URL.Path, "/log") && kubeutils.IsQueryParamPresent(request, "follow")) || kubeutils.IsQueryParamPresent(request, "watch")
}
