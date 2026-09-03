package operation

import (
	"context"
	"encoding/json"
	"time"

	"setpoint/internal/executor"
)

// SecretRef is an opaque reference only. Controlled Operations must not assume
// that a secure runtime secret-delivery channel exists until one is implemented
// and verified by the Agent runtime.
type SecretRef struct {
	RequirementID string `json:"requirement_id"`
	Reference     string `json:"reference"`
}

type Artifact struct {
	SchemaVersion string          `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

type FindingSeverity string

const (
	FindingInfo     FindingSeverity = "info"
	FindingWarning  FindingSeverity = "warning"
	FindingBlocking FindingSeverity = "blocking"
)

type Finding struct {
	Code     string          `json:"code"`
	Severity FindingSeverity `json:"severity"`
	Summary  string          `json:"summary"`
	Detail   string          `json:"detail,omitempty"`
	Target   *Target         `json:"target,omitempty"`
}

type EvidenceRef struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
}

type RuntimeInput struct {
	Executor   executor.CommandExecutor `json:"-"`
	Parameters json.RawMessage          `json:"parameters"`
	System     string                   `json:"system"`
	Targets    []Target                 `json:"targets"`
	SecretRefs []SecretRef              `json:"secret_refs,omitempty"`
}

type Discovery struct {
	Applicable bool          `json:"applicable"`
	Summary    string        `json:"summary"`
	Targets    []Target      `json:"targets"`
	Snapshot   Artifact      `json:"snapshot"`
	Findings   []Finding     `json:"findings,omitempty"`
	Evidence   []EvidenceRef `json:"evidence,omitempty"`
}

type Precheck struct {
	Passed   bool          `json:"passed"`
	Summary  string        `json:"summary"`
	Snapshot Artifact      `json:"snapshot"`
	Findings []Finding     `json:"findings,omitempty"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

type PlanStep struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Target         Target              `json:"target"`
	Action         string              `json:"action"`
	Checkpoint     string              `json:"checkpoint"`
	Writes         bool                `json:"writes"`
	RetrySafe      bool                `json:"retry_safe"`
	RollbackAction string              `json:"rollback_action,omitempty"`
	ExecutorNodeID string              `json:"executor_node_id,omitempty"`
	Barrier        StageBarrier        `json:"barrier,omitempty"`
	Mutation       StageMutation       `json:"mutation,omitempty"`
	Preconditions  []StagePrecondition `json:"preconditions,omitempty"`
}

type StageBarrier string

const StageBarrierAgentReconnect StageBarrier = "agent_reconnect"

type StageMutation string

const StageMutationVIPOwner StageMutation = "vip_owner"

type StagePreconditionKind string

const StagePreconditionVIPOwner StagePreconditionKind = "vip_owner"

type StagePrecondition struct {
	Kind              StagePreconditionKind `json:"kind"`
	ParticipantNodeID string                `json:"participant_node_id"`
	Verified          bool                  `json:"verified"`
	Evidence          []EvidenceRef         `json:"evidence"`
}

type Plan struct {
	SchemaVersion string        `json:"schema_version"`
	Summary       string        `json:"summary"`
	Steps         []PlanStep    `json:"steps"`
	Execution     Artifact      `json:"execution"`
	Findings      []Finding     `json:"findings,omitempty"`
	Evidence      []EvidenceRef `json:"evidence,omitempty"`
}

type Change struct {
	Target Target `json:"target"`
	Before string `json:"before"`
	After  string `json:"after"`
	Risk   string `json:"risk"`
}

type Impact struct {
	Summary             string        `json:"summary"`
	Risk                RiskLevel     `json:"risk"`
	Changes             []Change      `json:"changes"`
	RequiresDowntime    bool          `json:"requires_downtime"`
	RequiresWriteFence  bool          `json:"requires_write_fence"`
	EstimatedDuration   time.Duration `json:"estimated_duration"`
	EstimatedDataChange int64         `json:"estimated_data_change_bytes"`
}

type ApplyResult struct {
	Changed    bool          `json:"changed"`
	Checkpoint string        `json:"checkpoint"`
	State      Artifact      `json:"state"`
	Evidence   []EvidenceRef `json:"evidence,omitempty"`
}

type Verification struct {
	Passed   bool          `json:"passed"`
	Summary  string        `json:"summary"`
	Findings []Finding     `json:"findings,omitempty"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

type RollbackResult struct {
	Restored   bool          `json:"restored"`
	Checkpoint string        `json:"checkpoint"`
	State      Artifact      `json:"state"`
	Evidence   []EvidenceRef `json:"evidence,omitempty"`
}

type DiscoverInput struct{ Runtime RuntimeInput }
type PrecheckInput struct {
	Runtime   RuntimeInput
	Discovery Discovery
}
type PlanInput struct {
	Runtime   RuntimeInput
	Discovery Discovery
	Precheck  Precheck
}
type ImpactInput struct {
	Runtime RuntimeInput
	Plan    Plan
}
type ApplyInput struct {
	Runtime      RuntimeInput
	Plan         Plan
	Stage        *PlanStep
	Impact       Impact
	RestorePoint RestorePoint
	Lease        LeaseHandle
}
type VerifyInput struct {
	Runtime RuntimeInput
	Plan    Plan
	Stage   *PlanStep
	Apply   ApplyResult
}
type RollbackInput struct {
	Runtime      RuntimeInput
	Plan         Plan
	Stage        *PlanStep
	Apply        ApplyResult
	RestorePoint RestorePoint
	Lease        LeaseHandle
}
type VerifyRollbackInput struct {
	Runtime      RuntimeInput
	Plan         Plan
	Stage        *PlanStep
	Rollback     RollbackResult
	RestorePoint RestorePoint
}

// PlanningDefinition is the Agent-local, read-before-write portion of an
// OperationDefinition. The planning-only API stage deliberately transports
// only this contract; Apply and rollback have no task kind or HTTP route.
type PlanningDefinition interface {
	Descriptor
	Discover(context.Context, DiscoverInput) (Discovery, error)
	Precheck(context.Context, PrecheckInput) (Precheck, error)
	Plan(context.Context, PlanInput) (Plan, error)
	Impact(context.Context, ImpactInput) (Impact, error)
}

// OperationDefinition is the complete Controlled Operations contract. It is
// intentionally separate from the read-only CheckDefinition contract.
type OperationDefinition interface {
	Descriptor
	Discover(context.Context, DiscoverInput) (Discovery, error)
	Precheck(context.Context, PrecheckInput) (Precheck, error)
	Plan(context.Context, PlanInput) (Plan, error)
	Impact(context.Context, ImpactInput) (Impact, error)
	Apply(context.Context, ApplyInput) (ApplyResult, error)
	Verify(context.Context, VerifyInput) (Verification, error)
	Rollback(context.Context, RollbackInput) (RollbackResult, error)
	VerifyRollback(context.Context, VerifyRollbackInput) (Verification, error)
}
