package domain

import (
	"errors"
	"time"

	"setpoint/internal/trustedexec"
)

var ErrNotFound = errors.New("resource not found")

type NodeStatus string

const (
	NodeStatusOnline  NodeStatus = "online"
	NodeStatusOffline NodeStatus = "offline"
)

type Node struct {
	ID                     string                       `json:"id"`
	Hostname               string                       `json:"hostname"`
	OS                     string                       `json:"os"`
	OSVersion              string                       `json:"os_version"`
	Arch                   string                       `json:"arch"`
	AgentVersion           string                       `json:"agent_version"`
	ObservedSourceAddress  string                       `json:"observed_source_address"`
	SiteID                 string                       `json:"site_id,omitempty"`
	SiteName               string                       `json:"site_name,omitempty"`
	Tags                   []string                     `json:"tags"`
	Notes                  string                       `json:"notes"`
	TrustedExecutableRoots []trustedexec.ConfiguredRoot `json:"trusted_executable_roots"`
	RegisteredAt           time.Time                    `json:"registered_at"`
	LastSeenAt             time.Time                    `json:"last_seen_at"`
	Status                 NodeStatus                   `json:"status"`
}

type Registration struct {
	AgentID               string
	Hostname              string
	OS                    string
	OSVersion             string
	Arch                  string
	AgentVersion          string
	ObservedSourceAddress string
	ReceivedAt            time.Time
}
