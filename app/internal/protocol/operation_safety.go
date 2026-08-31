package protocol

import (
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type OperationActionScope struct {
	ClaimID string               `json:"claim_id"`
	RunID   string               `json:"run_id"`
	Action  task.OperationAction `json:"action"`
}

type OperationLeaseValidationRequest struct {
	Scope OperationActionScope `json:"scope"`
}

type OperationLeaseValidationResponse struct {
	Lease operation.LockLease `json:"lease"`
}

type OperationLedgerPutRequest struct {
	Scope OperationActionScope   `json:"scope"`
	Entry clickhouse.LedgerEntry `json:"entry"`
}

type OperationLedgerGetRequest struct {
	Scope OperationActionScope `json:"scope"`
	Key   clickhouse.LedgerKey `json:"key"`
}

type OperationLedgerGetResponse struct {
	Entry clickhouse.LedgerEntry `json:"entry"`
	Found bool                   `json:"found"`
}

type OperationLedgerListRunRequest struct {
	Scope OperationActionScope `json:"scope"`
}

type OperationLedgerListRunResponse struct {
	Entries []clickhouse.LedgerEntry `json:"entries"`
}

type OperationRestorePutRequest struct {
	Scope  OperationActionScope     `json:"scope"`
	Record clickhouse.RestoreRecord `json:"record"`
}

type OperationRestoreGetRequest struct {
	Scope OperationActionScope  `json:"scope"`
	Key   clickhouse.RestoreKey `json:"key"`
}

type OperationRestoreGetResponse struct {
	Record clickhouse.RestoreRecord `json:"record"`
	Found  bool                     `json:"found"`
}

type OperationRestoreListRunRequest struct {
	Scope OperationActionScope `json:"scope"`
}

type OperationRestoreListRunResponse struct {
	Records []clickhouse.RestoreRecord `json:"records"`
}
