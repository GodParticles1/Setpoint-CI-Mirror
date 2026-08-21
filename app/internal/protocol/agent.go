package protocol

import "time"

type RegistrationRequest struct {
	AgentID               string `json:"agent_id"`
	Hostname              string `json:"hostname"`
	OS                    string `json:"os"`
	OSVersion             string `json:"os_version"`
	Arch                  string `json:"arch"`
	AgentVersion          string `json:"agent_version"`
	ObservedSourceAddress string `json:"-"`
}

type RegistrationResponse struct {
	NodeID       string    `json:"node_id"`
	RegisteredAt time.Time `json:"registered_at"`
}

type HeartbeatResponse struct {
	AgentID        string    `json:"agent_id"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}
