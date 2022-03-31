package exec

import (
	"encoding/json"
	"fmt"
	"net/url"

	"gopkg.in/tomb.v2"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	kubedaemonutils "bastionzero.com/bctl/v1/bctl/daemon/plugin/kube/utils"
	"bastionzero.com/bctl/v1/bzerolib/logger"
	kubeaction "bastionzero.com/bctl/v1/bzerolib/plugin/kube"
	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
	stdin "bastionzero.com/bctl/v1/bzerolib/stream/stdreader"
	stdout "bastionzero.com/bctl/v1/bzerolib/stream/stdwriter"
)

type ExecSubAction string

const (
	ExecStart  ExecSubAction = "kube/exec/start"
	ExecInput  ExecSubAction = "kube/exec/input"
	ExecResize ExecSubAction = "kube/exec/resize"
	ExecStop   ExecSubAction = "kube/exec/stop"
)

const (
	EscChar = "^[" // ESC char
)

type ExecAction struct {
	logger *logger.Logger
	tmb    *tomb.Tomb
	closed bool

	// output channel to send all of our stream messages directly to datachannel
	streamOutputChan chan smsg.StreamMessage

	// To send input/resize to our exec sessions
	execStdinChannel  chan []byte
	execResizeChannel chan KubeExecResizeActionPayload

	serviceAccountToken string
	kubeHost            string
	targetGroups        []string
	targetUser          string
	logId               string
	requestId           string
}

func New(logger *logger.Logger,
	pluginTmb *tomb.Tomb,
	serviceAccountToken string,
	kubeHost string,
	targetGroups []string,
	targetUser string,
	ch chan smsg.StreamMessage) (*ExecAction, error) {

	return &ExecAction{
		logger:              logger,
		tmb:                 pluginTmb,
		closed:              false,
		streamOutputChan:    ch,
		execStdinChannel:    make(chan []byte, 10),
		execResizeChannel:   make(chan KubeExecResizeActionPayload, 10),
		serviceAccountToken: serviceAccountToken,
		kubeHost:            kubeHost,
		targetGroups:        targetGroups,
		targetUser:          targetUser,
	}, nil
}

func (e *ExecAction) Closed() bool {
	return e.closed
}

func (e *ExecAction) Receive(action string, actionPayload []byte) (string, []byte, error) {
	switch ExecSubAction(action) {

	// Start exec message required before anything else
	case ExecStart:
		var startExecRequest KubeExecStartActionPayload
		if err := json.Unmarshal(actionPayload, &startExecRequest); err != nil {
			rerr := fmt.Errorf("unable to unmarshal start exec message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		e.logId = startExecRequest.LogId
		e.requestId = startExecRequest.RequestId
		return e.StartExec(startExecRequest)

	case ExecInput:
		var execInputAction KubeStdinActionPayload
		if err := json.Unmarshal(actionPayload, &execInputAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stdin: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		if err := e.validateRequestId(execInputAction.RequestId); err != nil {
			return "", []byte{}, err
		}

		// Always feed in the exec stdin a chunk at a time (i.e. break up the byte array into chunks)
		for i := 0; i < len(execInputAction.Stdin); i += kubedaemonutils.ExecChunkSize {
			end := i + kubedaemonutils.ExecChunkSize
			if end > len(execInputAction.Stdin) {
				end = len(execInputAction.Stdin)
			}
			e.execStdinChannel <- execInputAction.Stdin[i:end]
		}
		return string(ExecInput), []byte{}, nil

	case ExecResize:
		var execResizeAction KubeExecResizeActionPayload
		if err := json.Unmarshal(actionPayload, &execResizeAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling resize message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		if err := e.validateRequestId(execResizeAction.RequestId); err != nil {
			return "", []byte{}, err
		}

		e.execResizeChannel <- execResizeAction
		return string(ExecResize), []byte{}, nil
	case ExecStop:
		var execStopAction KubeExecStopActionPayload
		if err := json.Unmarshal(actionPayload, &execStopAction); err != nil {
			rerr := fmt.Errorf("error unmarshaling stop message: %s", err)
			e.logger.Error(rerr)
			return "", []byte{}, rerr
		}

		if err := e.validateRequestId(execStopAction.RequestId); err != nil {
			return "", []byte{}, err
		}

		e.closed = true
		return string(ExecStop), []byte{}, nil
	default:
		rerr := fmt.Errorf("unhandled exec action: %v", action)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}
}

func (e *ExecAction) validateRequestId(requestId string) error {
	if requestId != e.requestId {
		rerr := fmt.Errorf("invalid request ID passed")
		e.logger.Error(rerr)
		return rerr
	}
	return nil
}

func (e *ExecAction) StartExec(startExecRequest KubeExecStartActionPayload) (string, []byte, error) {
	// Now open up our local exec session
	// Create the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		rerr := fmt.Errorf("error creating in-custer config: %s", err)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}

	// Always ensure that our targetUser is set
	if e.targetUser == "" {
		rerr := fmt.Errorf("target user field is not set")
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}

	// Add our impersonation information
	config.Impersonate = rest.ImpersonationConfig{
		UserName: e.targetUser,
		Groups:   e.targetGroups,
	}
	config.BearerToken = e.serviceAccountToken

	kubeExecApiUrl := e.kubeHost + startExecRequest.Endpoint
	kubeExecApiUrlParsed, err := url.Parse(kubeExecApiUrl)
	if err != nil {
		rerr := fmt.Errorf("could not parse kube exec url: %s", err)
		e.logger.Error(rerr)
		return "", []byte{}, rerr
	}

	// Turn it into a SPDY executor
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", kubeExecApiUrlParsed)
	if err != nil {
		return string(ExecStart), []byte{}, fmt.Errorf("error creating Spdy executor: %s", err)
	}

	stderrWriter := stdout.NewStdWriter(e.streamOutputChan, smsg.CurrentSchema, e.requestId, string(kubeaction.Exec), smsg.StdErr)
	stdoutWriter := stdout.NewStdWriter(e.streamOutputChan, smsg.CurrentSchema, e.requestId, string(kubeaction.Exec), smsg.StdOut)
	stdinReader := stdin.NewStdReader(smsg.StdIn, startExecRequest.RequestId, e.execStdinChannel)
	terminalSizeQueue := NewTerminalSizeQueue(startExecRequest.RequestId, e.execResizeChannel)

	// This function listens for a closed datachannel.  If the datachannel is closed, it doesn't necessarily mean
	// that the exec was properly closed, and because the below exec.Stream only returns when it's done, there's
	// no way to interrupt it or pass in a context. Therefore, we need to close the stream in order to pass an
	// io.EOF message to exec which will close the exec.Stream and that will close the go routine.
	// https://github.com/kubernetes/client-go/issues/554
	go func() {
		<-e.tmb.Dying()
		stdinReader.Close()
	}()

	// runs the exec interaction with the kube server
	go func() {
		if startExecRequest.IsTty {
			err = exec.Stream(remotecommand.StreamOptions{
				Stdin:             stdinReader,
				Stdout:            stdoutWriter,
				Stderr:            stderrWriter,
				TerminalSizeQueue: terminalSizeQueue,
				Tty:               true,
			})
		} else {
			err = exec.Stream(remotecommand.StreamOptions{
				Stdin:  stdinReader,
				Stdout: stdoutWriter,
				Stderr: stderrWriter,
			})
		}

		if err != nil {
			// Log the error
			rerr := fmt.Errorf("error in SPDY stream: %s", err)
			e.logger.Error(rerr)

			// Also write the error to our stdoutWriter so the user can see it
			stdoutWriter.Write([]byte(fmt.Sprint(err)))
		}

		// Now close the stream
		stdoutWriter.Write([]byte(EscChar))

		e.closed = true
	}()

	return string(ExecStart), []byte{}, nil
}
