package task

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/operation"
)

const (
	KindReadOnlyCheckTask     = "ReadOnlyCheckTask"
	KindOperationPlanningTask = "OperationPlanningTask"
)

type Phase string

const (
	PhasePending         Phase = "pending"
	PhaseClaimed         Phase = "claimed"
	PhaseRunning         Phase = "running"
	PhaseCancelRequested Phase = "cancel_requested"
	PhaseCanceled        Phase = "canceled"
	PhaseSucceeded       Phase = "succeeded"
	PhaseFailed          Phase = "failed"
)

var (
	ErrInvalidTransition   = errors.New("invalid task transition")
	ErrClaimMismatch       = errors.New("task claim does not match")
	ErrNodeMismatch        = errors.New("task does not belong to Agent")
	ErrIdempotencyConflict = errors.New("idempotency key is already used by a different task specification")
	ErrResultConflict      = errors.New("task already has a different terminal result")
)

type Metadata struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

type Spec struct {
	NodeID           string                  `json:"node_id"`
	PluginID         string                  `json:"plugin_id,omitempty"`
	Execution        *CheckExecutionContract `json:"execution_contract,omitempty"`
	ContractDigest   string                  `json:"contract_digest,omitempty"`
	OperationID      string                  `json:"operation_id,omitempty"`
	OperationVersion string                  `json:"operation_version,omitempty"`
	CapabilityDigest string                  `json:"capability_digest,omitempty"`
	Targets          []operation.Target      `json:"targets,omitempty"`
	Parameters       json.RawMessage         `json:"parameters"`
	SecretRefs       []operation.SecretRef   `json:"secret_refs,omitempty"`
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Status struct {
	Phase             Phase      `json:"phase"`
	ClaimID           string     `json:"claim_id,omitempty"`
	Attempt           int        `json:"attempt"`
	UpdatedAt         time.Time  `json:"updated_at"`
	ClaimedAt         *time.Time `json:"claimed_at,omitempty"`
	AcknowledgedAt    *time.Time `json:"acknowledged_at,omitempty"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	LastError         *Failure   `json:"last_error,omitempty"`
}

type Resource struct {
	APIVersion      string                    `json:"api_version"`
	Kind            string                    `json:"kind"`
	Metadata        Metadata                  `json:"metadata"`
	Spec            Spec                      `json:"spec"`
	Status          Status                    `json:"status"`
	Result          *CheckResult              `json:"result,omitempty"`
	OperationResult *operation.PlanningResult `json:"operation_result,omitempty"`
}

type CheckState string

const (
	CheckCompleted CheckState = "completed"
	CheckError     CheckState = "error"
)

type CheckResult struct {
	PluginID      string      `json:"plugin_id"`
	PluginVersion string      `json:"plugin_version"`
	State         CheckState  `json:"state"`
	StartedAt     time.Time   `json:"started_at"`
	CompletedAt   time.Time   `json:"completed_at"`
	Items         []CheckItem `json:"items"`
	Error         *Failure    `json:"error,omitempty"`
}

type ItemStatus string

const (
	ItemSafe          ItemStatus = "safe"
	ItemUnsafe        ItemStatus = "unsafe"
	ItemError         ItemStatus = "error"
	ItemManualReview  ItemStatus = "manual_review"
	ItemNotApplicable ItemStatus = "not_applicable"
)

func ValidItemStatus(status ItemStatus) bool {
	switch status {
	case ItemSafe, ItemUnsafe, ItemManualReview, ItemError, ItemNotApplicable:
		return true
	default:
		return false
	}
}

func NormalizeItem(item *CheckItem) {
	if item == nil || item.Status != "" {
		return
	}
	switch {
	case !item.Applicable:
		item.Status = ItemNotApplicable
	case item.Error != nil:
		item.Status = ItemError
	case item.Compliant != nil && *item.Compliant:
		item.Status = ItemSafe
	case item.Compliant != nil:
		item.Status = ItemUnsafe
	default:
		item.Status = ItemError
		item.Error = &Failure{Code: "check_status_missing", Message: "check item did not report a conclusion"}
	}
}

func ValidateItem(item CheckItem) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" || item.ExecutedAt.IsZero() {
		return errors.New("check item has missing identity or execution time")
	}
	if !ValidItemStatus(item.Status) {
		return fmt.Errorf("check item has invalid status %q", item.Status)
	}
	switch item.Status {
	case ItemSafe:
		if !item.Applicable || item.Compliant == nil || !*item.Compliant || item.Error != nil || item.ReviewReason != "" {
			return errors.New("safe check item has inconsistent applicability, compliance, or error")
		}
	case ItemUnsafe:
		if !item.Applicable || item.Compliant == nil || *item.Compliant || item.Error != nil || item.ReviewReason != "" {
			return errors.New("unsafe check item has inconsistent applicability, compliance, or error")
		}
	case ItemManualReview:
		if !item.Applicable || item.Compliant != nil || item.Error != nil || strings.TrimSpace(item.ReviewReason) == "" {
			return errors.New("manual review check item requires an applicable observation, no compliance value or error, and a review reason")
		}
	case ItemError:
		if item.Error == nil || item.Compliant != nil || item.ReviewReason != "" {
			return errors.New("error check item requires an error and no compliance value")
		}
	case ItemNotApplicable:
		if item.Applicable || item.Compliant != nil || item.Error != nil || item.ReviewReason != "" {
			return errors.New("not applicable check item has inconsistent fields")
		}
	}
	return nil
}

type CheckItem struct {
	ID                   string     `json:"id"`
	Status               ItemStatus `json:"status"`
	Name                 string     `json:"name"`
	CurrentValue         string     `json:"current_value"`
	RecommendedValue     string     `json:"recommended_value"`
	Compliant            *bool      `json:"compliant,omitempty"`
	Risk                 string     `json:"risk"`
	RiskDescription      string     `json:"risk_description"`
	Remediation          string     `json:"remediation"`
	EvidenceSummary      string     `json:"evidence_summary"`
	Applicable           bool       `json:"applicable"`
	ReviewReason         string     `json:"review_reason,omitempty"`
	SupportsAutomaticFix bool       `json:"supports_automatic_fix"`
	SupportsRollback     bool       `json:"supports_rollback"`
	RequiresRestart      bool       `json:"requires_restart"`
	MayAffectConnection  bool       `json:"may_affect_connection"`
	MayAffectBusiness    bool       `json:"may_affect_business"`
	ExecutedAt           time.Time  `json:"executed_at"`
	Error                *Failure   `json:"error,omitempty"`
}

type ResultSubmission struct {
	ClaimID         string                    `json:"claim_id"`
	Phase           Phase                     `json:"phase"`
	Result          *CheckResult              `json:"result,omitempty"`
	OperationResult *operation.PlanningResult `json:"operation_result,omitempty"`
}

func NewID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate task ID: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func ValidPhase(phase Phase) bool {
	switch phase {
	case PhasePending, PhaseClaimed, PhaseRunning, PhaseCancelRequested,
		PhaseCanceled, PhaseSucceeded, PhaseFailed:
		return true
	default:
		return false
	}
}

func Terminal(phase Phase) bool {
	return phase == PhaseCanceled || phase == PhaseSucceeded || phase == PhaseFailed
}

func ValidResultPhase(phase Phase) bool {
	return phase == PhaseCanceled || phase == PhaseSucceeded || phase == PhaseFailed
}

func Clone(resource Resource) Resource {
	resource.Spec.Parameters = append(json.RawMessage(nil), resource.Spec.Parameters...)
	resource.Spec.Execution = CloneCheckExecutionContract(resource.Spec.Execution)
	resource.Spec.Targets = append([]operation.Target(nil), resource.Spec.Targets...)
	resource.Spec.SecretRefs = append([]operation.SecretRef(nil), resource.Spec.SecretRefs...)
	if resource.Status.LastError != nil {
		copy := *resource.Status.LastError
		resource.Status.LastError = &copy
	}
	if resource.Result != nil {
		result := cloneCheckResult(*resource.Result)
		resource.Result = &result
	}
	if resource.OperationResult != nil {
		encoded, _ := json.Marshal(resource.OperationResult)
		var result operation.PlanningResult
		_ = json.Unmarshal(encoded, &result)
		resource.OperationResult = &result
	}
	return resource
}

func cloneCheckResult(result CheckResult) CheckResult {
	result.Items = append([]CheckItem(nil), result.Items...)
	for index := range result.Items {
		if result.Items[index].Compliant != nil {
			value := *result.Items[index].Compliant
			result.Items[index].Compliant = &value
		}
		if result.Items[index].Error != nil {
			failure := *result.Items[index].Error
			result.Items[index].Error = &failure
		}
	}
	if result.Error != nil {
		failure := *result.Error
		result.Error = &failure
	}
	return result
}
