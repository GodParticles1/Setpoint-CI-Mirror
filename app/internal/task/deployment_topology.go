package task

import (
	"time"

	"setpoint/internal/deploymenttopology"
)

const KindDeploymentTopologyDiscoveryTask = "DeploymentTopologyDiscoveryTask"

type DeploymentTopologyResult struct {
	StartedAt   time.Time                 `json:"started_at"`
	CompletedAt time.Time                 `json:"completed_at"`
	Topology    deploymenttopology.Result `json:"topology"`
	Error       *Failure                  `json:"error,omitempty"`
}
