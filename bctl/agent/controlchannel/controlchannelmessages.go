package controlchannel

type AliveCheckClusterToBastionMessage struct {
	Alive        bool     `json:"alive"`
	ClusterUsers []string `json:"clusterUsers"`
}

type RegisterAgentMessage struct {
	PublicKey      string `json:"publicKey"`
	ActivationCode string `json:"activationCode"`
	AgentVersion   string `json:"agentVersion"`
	OrgId          string `json:"orgId"`
	EnvironmentId  string `json:"environmentId"`
	ClusterName    string `json:"clusterName"`
	ClusterId      string `json:"clusterId"`
}

type HealthCheckMessage struct {
	ClusterName string `json:"clusterName"`
}

// websocket and datachannel management payloads
type OpenWebsocketMessage struct {
	DaemonWebsocketId string `json:"daemonWebsocketId"`
	ConnectionNodeId  string `json:"connectionNodeId"`
	Token             string `json:"token"`
	Type              string `json:"type"`
}

type CloseWebsocketMessage struct {
	DaemonWebsocketId string `json:"daemonWebsocketId"`
}

type OpenDataChannelMessage struct {
	DataChannelId     string `json:"dataChannelId"`
	DaemonWebsocketId string `json:"daemonWebsocketId"`
	Syn               []byte `json:"syn"`
}

type CloseDataChannelMessage struct {
	DataChannelId string `json:"dataChannelId"`
	ConnectionId  string `json:"connectionId"`
}

type DataChannelReadyMessage struct {
	DataChannelId string `json:"dataChannelId"`
	ConnectionId  string `json:"connectionId"`
}
