package stdreader

import (
	"bytes"
	"io"

	smsg "bastionzero.com/bctl/v1/bzerolib/stream/message"
)

var (
	EndStreamBytes = []byte{0x62, 0x61, 0x73, 0x74, 0x69, 0x6f, 0x6e, 0x7a, 0x65, 0x72, 0x6f} // "BastionZero"
)

// Stdin
type StdReader struct {
	StreamType   smsg.StreamType
	RequestId    string
	stdinChannel chan []byte
	doneChannel  chan bool
}

func NewStdReader(streamType smsg.StreamType, requestId string, stdinChannel chan []byte) *StdReader {
	stdin := &StdReader{
		StreamType:   streamType,
		RequestId:    requestId,
		stdinChannel: stdinChannel,
		doneChannel:  make(chan bool),
	}

	return stdin
}

func (r *StdReader) Close() {
	r.doneChannel <- true
}

func (r *StdReader) Read(p []byte) (int, error) {
	// Listen for data on our stdinChannel
	if bytes.Equal(p, EndStreamBytes) {
		return 1, io.EOF
	}
	select {
	case stdin := <-r.stdinChannel:
		n := copy(p, stdin)
		return n, nil
	case <-r.doneChannel:
		return 1, io.EOF
	}
}
