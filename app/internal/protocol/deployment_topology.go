package protocol

import (
	"time"

	"setpoint/internal/deploymenttopology"
	"setpoint/internal/task"
)

const DeploymentTopologyDiscoveryKind = "DeploymentTopologyDiscovery"

type CreateDeploymentTopologyDiscoveryRequest struct {
	APIVersion string                                    `json:"api_version"`
	Kind       string                                    `json:"kind"`
	Metadata   CreateDeploymentTopologyDiscoveryMetadata `json:"metadata"`
}

type CreateDeploymentTopologyDiscoveryMetadata struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type DeploymentTopologyDiscoveryResource struct {
	APIVersion  string                     `json:"api_version"`
	Kind        string                     `json:"kind"`
	ID          string                     `json:"id"`
	NodeID      string                     `json:"node_id"`
	Phase       task.Phase                 `json:"phase"`
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
	CompletedAt *time.Time                 `json:"completed_at,omitempty"`
	Topology    *deploymenttopology.Result `json:"topology,omitempty"`
	Error       *task.Failure              `json:"error,omitempty"`
}
