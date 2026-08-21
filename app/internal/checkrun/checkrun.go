package checkrun

import (
	"encoding/json"
	"time"

	"setpoint/internal/task"
)

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

type Resource struct {
	APIVersion string          `json:"api_version"`
	Kind       string          `json:"kind"`
	Metadata   Metadata        `json:"metadata"`
	Spec       Spec            `json:"spec"`
	Status     Status          `json:"status"`
	Tasks      []task.Resource `json:"tasks,omitempty"`
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
