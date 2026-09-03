package protocol

import (
	"encoding/json"

	"setpoint/internal/operation"
	"setpoint/internal/operationbatch"
	"setpoint/internal/operationrun"
)

type CreateOperationRunRequest struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Metadata   struct {
		IdempotencyKey string `json:"idempotency_key"`
	} `json:"metadata"`
	Spec struct {
		OperationID        string                `json:"operation_id"`
		NodeID             string                `json:"node_id"`
		ParticipantNodeIDs []string              `json:"participant_node_ids,omitempty"`
		Targets            []operation.Target    `json:"targets"`
		Parameters         json.RawMessage       `json:"parameters"`
		SecretRefs         []operation.SecretRef `json:"secret_refs,omitempty"`
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

type ConfirmOperationBatchRequest struct {
	BatchID                    string `json:"batch_id"`
	SourceCheckRunID           string `json:"source_check_run_id"`
	ConfirmationIdempotencyKey string `json:"confirmation_idempotency_key"`
	Members                    []struct {
		TaskID     string `json:"task_id"`
		CheckID    string `json:"check_id"`
		NodeID     string `json:"node_id"`
		RunID      string `json:"run_id"`
		PlanDigest string `json:"plan_digest"`
	} `json:"members"`
}

type OperationBatchConfirmationResponse struct {
	Receipt operationbatch.Receipt  `json:"receipt"`
	Runs    []operationrun.Resource `json:"runs"`
}

type OperationBatchConfirmationListResponse struct {
	Confirmations []OperationBatchConfirmationResponse `json:"confirmations"`
	Limit         int                                  `json:"limit"`
	Offset        int                                  `json:"offset"`
}
