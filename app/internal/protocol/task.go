package protocol

import "setpoint/internal/task"

type CreateTaskRequest struct {
	APIVersion string             `json:"api_version"`
	Kind       string             `json:"kind"`
	Metadata   CreateTaskMetadata `json:"metadata"`
	Spec       task.Spec          `json:"spec"`
}

type CreateTaskMetadata struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type ClaimTaskResponse struct {
	Task task.Resource `json:"task"`
}

type AcknowledgeTaskRequest struct {
	ClaimID string `json:"claim_id"`
}

type TaskResultRequest = task.ResultSubmission

type TaskListResponse struct {
	Tasks []task.Resource `json:"tasks"`
}
