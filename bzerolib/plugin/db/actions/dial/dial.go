package dial

type DialSubAction string

const (
	DialStart DialSubAction = "db/dial/start"
	DialInput DialSubAction = "db/dial/input"
	DialStop  DialSubAction = "db/dial/stop"
)

type DialActionPayload struct {
	RequestId string `json:"requestId"`
}

type DialInputActionPayload struct {
	RequestId      string `json:"requestId"`
	SequenceNumber int    `json:"sequenceNumber"`
	Data           string `json:"data"`
}
