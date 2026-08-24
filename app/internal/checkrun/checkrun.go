package checkrun

import (
	"encoding/json"
	"strings"
	"time"

	"setpoint/internal/task"
)

const icmpRedirectRuntimeRepairOperationID = "linux.network.icmp_redirects.runtime_repair"

type Phase string

const (
	PhasePending       Phase = "pending"
	PhaseRunning       Phase = "running"
	PhaseCompleted     Phase = "completed"
	PhasePartialFailed Phase = "partial_failed"
	PhaseCanceled      Phase = "canceled"
)

type Metadata struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
}

type Spec struct {
	NodeIDs    []string                   `json:"node_ids"`
	CheckIDs   []string                   `json:"check_ids"`
	BundleIDs  []string                   `json:"bundle_ids,omitempty"`
	PolicyIDs  []string                   `json:"policy_ids,omitempty"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

type Counts struct {
	TotalTasks     int `json:"total_tasks"`
	PendingTasks   int `json:"pending_tasks"`
	RunningTasks   int `json:"running_tasks"`
	CompletedTasks int `json:"completed_tasks"`
	CanceledTasks  int `json:"canceled_tasks"`
	Safe           int `json:"safe"`
	Unsafe         int `json:"unsafe"`
	ManualReview   int `json:"manual_review"`
	Error          int `json:"error"`
	NotApplicable  int `json:"not_applicable"`
}

type Status struct {
	Phase     Phase     `json:"phase"`
	Counts    Counts    `json:"counts"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RemediationConstraints struct {
	Options []string `json:"options,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
}

type RemediationOffer struct {
	CheckRunID                 string                 `json:"check_run_id"`
	TaskID                     string                 `json:"task_id"`
	CheckID                    string                 `json:"check_id"`
	NodeID                     string                 `json:"node_id"`
	CurrentValue               string                 `json:"current_value"`
	ExistingRecommendedValue   string                 `json:"existing_recommended_value"`
	RecommendedValueForThisRun string                 `json:"recommended_value_for_this_run"`
	RecommendationReason       string                 `json:"recommendation_reason"`
	Availability               string                 `json:"availability"`
	Editable                   bool                   `json:"editable"`
	ParameterType              string                 `json:"parameter_type,omitempty"`
	Constraints                RemediationConstraints `json:"constraints"`
	SupportsAutomaticFix       bool                   `json:"supports_automatic_fix"`
	SupportsRollback           bool                   `json:"supports_rollback"`
	Risk                       string                 `json:"risk"`
	RequiresRestart            bool                   `json:"requires_restart"`
	MayAffectConnection        bool                   `json:"may_affect_connection"`
	MayAffectBusiness          bool                   `json:"may_affect_business"`
	OperationID                string                 `json:"operation_id,omitempty"`
	OperationParameters        map[string]string      `json:"operation_parameters,omitempty"`
	BlockReason                string                 `json:"block_reason,omitempty"`
}

type Resource struct {
	APIVersion        string             `json:"api_version"`
	Kind              string             `json:"kind"`
	Metadata          Metadata           `json:"metadata"`
	Spec              Spec               `json:"spec"`
	Status            Status             `json:"status"`
	Tasks             []task.Resource    `json:"tasks,omitempty"`
	RemediationOffers []RemediationOffer `json:"remediation_offers,omitempty"`
}

func BuildRemediationOffers(run Resource) []RemediationOffer {
	offers := make([]RemediationOffer, 0)
	for _, resource := range run.Tasks {
		if resource.Result == nil {
			continue
		}
		for _, item := range resource.Result.Items {
			supportsAutomaticFix, supportsRollback, operationID, parameters := approvedRepairCapability(item)
			offer := RemediationOffer{
				CheckRunID: run.Metadata.ID, TaskID: resource.Metadata.ID, CheckID: item.ID, NodeID: resource.Spec.NodeID,
				CurrentValue: item.CurrentValue, ExistingRecommendedValue: item.RecommendedValue,
				RecommendedValueForThisRun: item.RecommendedValue,
				RecommendationReason:       strings.TrimSpace(item.Remediation),
				Availability:               "manual_only", Editable: false,
				SupportsAutomaticFix: supportsAutomaticFix, SupportsRollback: supportsRollback,
				Risk: item.Risk, RequiresRestart: item.RequiresRestart,
				MayAffectConnection: item.MayAffectConnection, MayAffectBusiness: item.MayAffectBusiness,
				OperationID: operationID, OperationParameters: parameters,
			}
			if offer.RecommendationReason == "" {
				offer.RecommendationReason = strings.TrimSpace(item.EvidenceSummary)
			}
			switch {
			case item.Status != task.ItemUnsafe:
				offer.BlockReason = "check result is not an unsafe finding"
			case strings.TrimSpace(item.CurrentValue) == "":
				offer.BlockReason = "current value is unavailable"
			case strings.TrimSpace(item.RecommendedValue) == "":
				offer.BlockReason = "recommended value is unavailable"
			case item.MayAffectConnection || item.MayAffectBusiness:
				offer.BlockReason = "connection or business impact requires manual handling"
			case operationID == "" || len(parameters) == 0:
				offer.BlockReason = "no approved automatic repair capability matches this result"
			case !supportsAutomaticFix:
				offer.BlockReason = "approved repair capability does not support automatic fix"
			case !supportsRollback:
				offer.BlockReason = "approved repair capability does not provide rollback"
			default:
				offer.Availability = "actionable"
				offer.ParameterType = "string"
				offer.Constraints.Options = []string{item.RecommendedValue}
			}
			offers = append(offers, offer)
		}
	}
	return offers
}

func approvedRepairCapability(item task.CheckItem) (bool, bool, string, map[string]string) {
	if item.Status != task.ItemUnsafe || item.RecommendedValue != "runtime=0; persisted=0" || item.CurrentValue != "runtime=1; persisted=0" || item.MayAffectConnection || item.MayAffectBusiness || item.RequiresRestart {
		return false, false, "", nil
	}
	switch item.ID {
	case "net.ipv4.conf.all.accept_redirects.persisted",
		"net.ipv4.conf.default.accept_redirects.persisted",
		"net.ipv4.conf.all.send_redirects.persisted",
		"net.ipv4.conf.default.send_redirects.persisted":
		return true, true, icmpRedirectRuntimeRepairOperationID, map[string]string{
			"check_id": item.ID, "target_value": item.RecommendedValue,
		}
	default:
		return false, false, "", nil
	}
}

type CancelOutcome string

const (
	CancelOutcomeCanceled        CancelOutcome = "canceled"
	CancelOutcomeRequested       CancelOutcome = "cancel_requested"
	CancelOutcomeAlreadyTerminal CancelOutcome = "already_terminal"
	CancelOutcomeFailed          CancelOutcome = "failed"
)

type CancelTaskResult struct {
	TaskID  string        `json:"task_id"`
	Outcome CancelOutcome `json:"outcome"`
	Phase   task.Phase    `json:"phase"`
	Error   *task.Failure `json:"error,omitempty"`
}

type CancelReport struct {
	TotalTasks           int                `json:"total_tasks"`
	CanceledTasks        int                `json:"canceled_tasks"`
	CancelRequestedTasks int                `json:"cancel_requested_tasks"`
	AlreadyTerminalTasks int                `json:"already_terminal_tasks"`
	FailedTasks          int                `json:"failed_tasks"`
	Results              []CancelTaskResult `json:"results"`
}

type CancelResponse struct {
	Run    Resource     `json:"run"`
	Report CancelReport `json:"cancel_report"`
}

func Aggregate(tasks []task.Resource) Status {
	status := Status{Phase: PhasePending, Counts: Counts{TotalTasks: len(tasks)}}
	if len(tasks) == 0 {
		return status
	}
	allTerminal := true
	allCanceled := true
	latest := time.Time{}
	for _, current := range tasks {
		if current.Status.UpdatedAt.After(latest) {
			latest = current.Status.UpdatedAt
		}
		switch current.Status.Phase {
		case task.PhasePending:
			status.Counts.PendingTasks++
			allTerminal = false
			allCanceled = false
		case task.PhaseClaimed, task.PhaseRunning, task.PhaseCancelRequested:
			status.Counts.RunningTasks++
			allTerminal = false
			allCanceled = false
		case task.PhaseCanceled:
			status.Counts.CanceledTasks++
		case task.PhaseSucceeded, task.PhaseFailed:
			status.Counts.CompletedTasks++
			allCanceled = false
		}
		if current.Result == nil {
			continue
		}
		for _, item := range current.Result.Items {
			switch item.Status {
			case task.ItemSafe:
				status.Counts.Safe++
			case task.ItemUnsafe:
				status.Counts.Unsafe++
			case task.ItemManualReview:
				status.Counts.ManualReview++
			case task.ItemError:
				status.Counts.Error++
			case task.ItemNotApplicable:
				status.Counts.NotApplicable++
			}
		}
	}
	status.UpdatedAt = latest
	if allTerminal {
		if allCanceled {
			status.Phase = PhaseCanceled
		} else if status.Counts.CanceledTasks > 0 || hasFailedTask(tasks) {
			status.Phase = PhasePartialFailed
		} else {
			status.Phase = PhaseCompleted
		}
	} else if status.Counts.RunningTasks > 0 || status.Counts.CompletedTasks > 0 || status.Counts.CanceledTasks > 0 {
		status.Phase = PhaseRunning
	}
	return status
}

func hasFailedTask(tasks []task.Resource) bool {
	for _, current := range tasks {
		if current.Status.Phase == task.PhaseFailed {
			return true
		}
	}
	return false
}
