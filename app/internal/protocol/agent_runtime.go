package protocol

const (
	AgentRuntimeReadyPath       = "/readyz"
	AgentRuntimeReadyService    = "setpoint-agent-listener"
	AgentRuntimeContractVersion = "v1"
)

type AgentRuntimeReadyResponse struct {
	Status          string `json:"status"`
	Service         string `json:"service"`
	ContractVersion string `json:"contract_version"`
}
