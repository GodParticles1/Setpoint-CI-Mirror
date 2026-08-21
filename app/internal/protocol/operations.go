package protocol

import (
	"encoding/json"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
)

type CreateOperationRunRequest struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Metadata   struct {
		IdempotencyKey string `json:"idempotency_key"`
	} `json:"metadata"`
	Spec struct {
		OperationID string                `json:"operation_id"`
		NodeID      string                `json:"node_id"`
		Targets     []operation.Target    `json:"targets"`
		Parameters  json.RawMessage       `json:"parameters"`
		SecretRefs  []operation.SecretRef `json:"secret_refs,omitempty"`
	} `json:"spec"`
}

type ConfirmOperationRunRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	PlanDigest     string `json:"plan_digest"`
}

type OperationRunListResponse struct {
	Runs   []operationrun.Resource `json:"runs"`
	Limit  int                     `json:"limit"`
	Offset int                     `json:"offset"`
}
