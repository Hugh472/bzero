package webdial

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"gopkg.in/tomb.v2"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

type WebDialSubAction string

const (
	WebDialStart  WebDialSubAction = "web/dial/start"
	WebDialDataIn WebDialSubAction = "web/dial/datain"
)

type WebDial struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	requestId     string
	remoteAddress *net.TCPAddr

	remoteConnection *net.TCPConn
}

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	ch chan smsg.StreamMessage,
	raddr *net.TCPAddr) (*WebDial, error) {

	return &WebDial{
		logger:           logger,
		tmb:              pluginTmb,
		closed:           false,
		streamOutputChan: ch,
		remoteAddress:    raddr,
	}, nil
}

func (s *WebDial) Closed() bool {
	return s.closed
}

func (e *WebDial) Receive(action string, actionPayload []byte) (string, []byte, error) {
	switch WebDialSubAction(action) {
	case WebDialStart:
		var webDialActionRequest WebDialActionPayload
		if err := json.Unmarshal(actionPayload, &webDialActionRequest); err != nil {
			rerr := fmt.Errorf("malformed web dial Action payload %v", actionPayload)
			e.logger.Error(rerr)
			return action, []byte{}, rerr
		}

		return e.StartDial(webDialActionRequest, action)
	case WebDialDataIn:
		// Deserialize the action payload, the only action passed is DataIn
		var dataIn WebDataInActionPayload
		if err := json.Unmarshal(actionPayload, &dataIn); err != nil {
			rerr := fmt.Errorf("unable to unmarshal dataIn message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		// First validate the requestId
		if err := e.validateRequestId(dataIn.RequestId); err != nil {
			return "", []byte{}, err
		}

		// Then send the data to our remote connection, decode the data first
		dataToWrite, _ := base64.StdEncoding.DecodeString(dataIn.Data)

		// Send this data to our remote connection
		e.logger.Info("Received data from bastion, forwarding to remote tcp connection")
		_, err := e.remoteConnection.Write(dataToWrite)
		if err != nil {
			e.logger.Errorf("error writing to to remote connection: %v", err)
			return "", []byte{}, err
		}

		return "", []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled stream action: %v", action)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (e *WebDial) StartDial(dialActionRequest WebDialActionPayload, action string) (string, []byte, error) {
	// Set our requestId
	e.requestId = dialActionRequest.RequestId

	// For each start, call the dial the TCP address
	remoteConnection, err := net.DialTCP("tcp", nil, e.remoteAddress)
	if err != nil {
		e.logger.Errorf("Failed to dial remote address: %s", err)
		// Let the agent know that there was an error
		return action, []byte{}, err
	}

	// Setup a go routine to listen for messages coming from this local connection and forward to the client
	// TODO: Setup tomb for this to be cancelled?
	sequenceNumber := 1

	go func() {
		buff := make([]byte, 0xffff)
		for {
			n, err := remoteConnection.Read(buff)
			if err != nil {
				if err != io.EOF {
					e.logger.Errorf("Read failed '%s'\n", err)
				}

				// Let our daemon know that we have got the error and we need to close the connection
				message := smsg.StreamMessage{
					Type:           string(smsg.WebAgentClose),
					RequestId:      e.requestId,
					SequenceNumber: sequenceNumber,
					Content:        "", // No content for webAgent Close
					LogId:          "", // No log id for web messages
				}
				e.streamOutputChan <- message

				// Ensure that we close the dial action
				e.closed = true
				return
			}

			tcpBytesBuffer := buff[:n]

			e.logger.Infof("Received %d bytes from local tcp connection, sending to bastion", n)

			// Now send this to bastion
			str := base64.StdEncoding.EncodeToString(tcpBytesBuffer)
			message := smsg.StreamMessage{
				Type:           string(smsg.WebOut),
				RequestId:      e.requestId,
				SequenceNumber: sequenceNumber,
				Content:        str,
				LogId:          "", // No log id for web messages
			}
			e.streamOutputChan <- message

			sequenceNumber += 1
		}
	}()

	// Update our remote connection
	e.remoteConnection = remoteConnection
	return action, []byte{}, nil
}

func (e *WebDial) validateRequestId(requestId string) error {
	if requestId != e.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		e.logger.Error(rerr)
		return rerr
	}
	return nil
}