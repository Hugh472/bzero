package db

import "net"

type DbAction string

const (
	Dial DbAction = "dial"
	// Start  DbAction = "start"
	// DataIn DbAction = "datain"
)

type DbActionParams struct {
	TargetPort     int    `json:"targetPort"`
	TargetHost     string `json:"targetHost"`
	TargetHostName string `json:"targetHostName"`
}

type DbFood struct {
	Action DbAction
	Conn   *net.TCPConn
}
