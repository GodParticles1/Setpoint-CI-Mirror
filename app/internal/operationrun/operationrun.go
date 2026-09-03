package operationrun

import (
	"encoding/json"
	"time"

	"setpoint/internal/operation"
)

type Metadata struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

type Spec struct {
	OperationID        string                `json:"operation_id"`
	OperationVersion   string                `json:"operation_version"`
	CapabilityDigest   string                `json:"capability_digest"`
	NodeID             string                `json:"node_id"`
	ParticipantNodeIDs []string              `json:"participant_node_ids"`
	Targets            []operation.Target    `json:"targets"`
	Parameters         json.RawMessage       `json:"parameters"`
	SecretRefs         []operation.SecretRef `json:"secret_refs,omitempty"`
}

type Recovery struct {
	Code         string `json:"code"`
	Checkpoint   string `json:"checkpoint,omitempty"`
	SafeNext     string `json:"safe_next_action"`
	ManualReview bool   `json:"manual_review"`
}

const (
	RecoveryCancellationRequested = "cancellation_requested"
	RecoveryCancellationReconcile = "cancellation_requires_reconcile"
)

type Availability struct {
	Planning       bool   `json:"planning"`
	Apply          bool   `json:"apply"`
	BlockCode      string `json:"block_code"`
	SecretDelivery bool   `json:"secret_delivery"`
}

type DefinitionResource struct {
	APIVersion       string             `json:"api_version"`
	Kind             string             `json:"kind"`
	Metadata         operation.Metadata `json:"metadata"`
	CapabilityDigest string             `json:"capability_digest"`
	Availability     Availability       `json:"availability"`
}

type Status struct {
	State          operation.State  `json:"state"`
	Checkpoint     string           `json:"checkpoint"`
	TaskID         string           `json:"task_id"`
	UpdatedAt      time.Time        `json:"updated_at"`
	ApplyAvailable bool             `json:"apply_available"`
	Block          *operation.Block `json:"block,omitempty"`
	Recovery       *Recovery        `json:"recovery,omitempty"`
}

// ExecutionSnapshot stores only durable, non-secret lifecycle facts. It is
// deliberately independent from the Platform task transport so later Agent
// integration can persist the same Operation lifecycle without inventing a
// second business state model.
type ExecutionSnapshot struct {
	RestorePoint         *operation.RestorePoint   `json:"restore_point,omitempty"`
	Apply                *operation.ApplyResult    `json:"apply,omitempty"`
	Verification         *operation.Verification   `json:"verification,omitempty"`
	Rollback             *operation.RollbackResult `json:"rollback,omitempty"`
	RollbackVerification *operation.Verification   `json:"rollback_verification,omitempty"`
	Stages               []StageExecutionSnapshot  `json:"stages,omitempty"`
}

type StageExecutionSnapshot struct {
	StageIndex             int                       `json:"stage_index"`
	StageID                string                    `json:"stage_id"`
	ExecutorNodeID         string                    `json:"executor_node_id"`
	RestorePointAt         time.Time                 `json:"restore_point_at,omitempty"`
	ApplyAt                time.Time                 `json:"apply_at,omitempty"`
	VerificationAt         time.Time                 `json:"verification_at,omitempty"`
	RollbackAt             time.Time                 `json:"rollback_at,omitempty"`
	RollbackVerificationAt time.Time                 `json:"rollback_verification_at,omitempty"`
	RestorePoint           *operation.RestorePoint   `json:"restore_point,omitempty"`
	Apply                  *operation.ApplyResult    `json:"apply,omitempty"`
	Verification           *operation.Verification   `json:"verification,omitempty"`
	Rollback               *operation.RollbackResult `json:"rollback,omitempty"`
	RollbackVerification   *operation.Verification   `json:"rollback_verification,omitempty"`
}

type Resource struct {
	APIVersion string               `json:"api_version"`
	Kind       string               `json:"kind"`
	Metadata   Metadata             `json:"metadata"`
	Spec       Spec                 `json:"spec"`
	Status     Status               `json:"status"`
	Discovery  *operation.Discovery `json:"discovery,omitempty"`
	Precheck   *operation.Precheck  `json:"precheck,omitempty"`
	Plan       *operation.Plan      `json:"plan,omitempty"`
	Impact     *operation.Impact    `json:"impact,omitempty"`
	PlanDigest string               `json:"plan_digest,omitempty"`
	Execution  *ExecutionSnapshot   `json:"execution,omitempty"`
}
