package protocol

import "setpoint/internal/bootstrap"

type NodeBootstrapGatewayRequest struct {
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type NodeBootstrapProbeRequest struct {
	Address  string                       `json:"address"`
	Port     uint16                       `json:"port"`
	Username string                       `json:"username"`
	Password string                       `json:"password"`
	Gateway  *NodeBootstrapGatewayRequest `json:"gateway,omitempty"`
}

type NodeBootstrapProbeResponse struct {
	HostKeyFingerprint        string                   `json:"host_key_fingerprint"`
	GatewayHostKeyFingerprint string                   `json:"gateway_host_key_fingerprint,omitempty"`
	OS                        string                   `json:"os"`
	OSVersion                 string                   `json:"os_version"`
	Arch                      string                   `json:"arch"`
	Username                  string                   `json:"username"`
	UID                       int                      `json:"uid"`
	Mode                      string                   `json:"mode"`
	Home                      string                   `json:"home"`
	AgentPresent              bool                     `json:"agent_present"`
	TargetInstallProfile      bootstrap.InstallProfile `json:"target_install_profile"`
}

type NodeBootstrapApplyRequest struct {
	Address                           string                       `json:"address"`
	Port                              uint16                       `json:"port"`
	Username                          string                       `json:"username"`
	Password                          string                       `json:"password"`
	Gateway                           *NodeBootstrapGatewayRequest `json:"gateway,omitempty"`
	ExpectedHostKeyFingerprint        string                       `json:"expected_host_key_fingerprint"`
	ExpectedGatewayHostKeyFingerprint string                       `json:"expected_gateway_host_key_fingerprint,omitempty"`
	SiteID                            string                       `json:"site_id,omitempty"`
}

type NodeBootstrapApplyResponse struct {
	NodeID       string `json:"node_id"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	OSVersion    string `json:"os_version"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	Status       string `json:"status"`
	SiteID       string `json:"site_id,omitempty"`
}
