package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/sysctlrepair"
	"setpoint/internal/operationrun"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type productOperationRepository interface {
	OperationRunRepository
	GetTask(context.Context, string) (task.Resource, error)
	SaveOperationExecutionCheckpoint(context.Context, string, operation.State, string, operationrun.ExecutionSnapshot, *operationrun.Recovery, operation.JournalEntry, time.Time) (operationrun.Resource, error)
	ContinueOperationRun(context.Context, string, string, operation.State, string, task.Resource, operation.JournalEntry, time.Time) (operationrun.Resource, error)
	List(context.Context, string) ([]operation.JournalEntry, error)
}

type productLeaseSupervisor interface {
	Acquire(context.Context, string, []operation.Target) (operation.LockLease, error)
	Resume(context.Context, string, []operation.Target) (operation.LockLease, error)
	CurrentLeaseByOwner(context.Context, string) (operation.LockLease, bool, error)
	Release(context.Context, string) error
}

type ProductOperations struct {
	base  *OperationsService
	runs  productOperationRepository
	lease productLeaseSupervisor
	now   func() time.Time
}

func NewProductOperations(base *OperationsService, runs productOperationRepository, lease productLeaseSupervisor) (*ProductOperations, error) {
	if base == nil || runs == nil || lease == nil {
		return nil, errors.New("base operations service, operation repository and lease supervisor are required")
	}
	return &ProductOperations{base: base, runs: runs, lease: lease, now: time.Now}, nil
}

func (service *ProductOperations) ListOperations() ([]operationrun.DefinitionResource, error) {
	resources, err := service.base.ListOperations()
	if err != nil {
		return nil, err
	}
	for index := range resources {
		resources[index] = productOperationAvailability(resources[index])
	}
	return resources, nil
}

func (service *ProductOperations) GetOperation(id string) (operationrun.DefinitionResource, error) {
	resource, err := service.base.GetOperation(id)
	if err != nil {
		return operationrun.DefinitionResource{}, err
	}
	return productOperationAvailability(resource), nil
}

func productOperationAvailability(resource operationrun.DefinitionResource) operationrun.DefinitionResource {
	if resource.Metadata.ID == sysctlrepair.ID {
		resource.Availability.Apply = true
		resource.Availability.BlockCode = ""
	}
	return resource
}

func (service *ProductOperations) CreateOperationRun(ctx context.Context, request protocol.CreateOperationRunRequest) (operationrun.Resource, bool, error) {
	return service.base.CreateOperationRun(ctx, request)
}

func (service *ProductOperations) GetOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	return service.base.GetOperationRun(ctx, id)
}

func (service *ProductOperations) ListOperationRuns(ctx context.Context, options protocol.ListOptions) ([]operationrun.Resource, protocol.ListOptions, error) {
	return service.base.ListOperationRuns(ctx, options)
}

func (service *ProductOperations) ConfirmOperationRun(ctx context.Context, id string, request protocol.ConfirmOperationRunRequest) (operationrun.Resource, error) {
	if err := validateIdentifier(strings.TrimSpace(request.IdempotencyKey)); err != nil {
		return operationrun.Resource{}, &ValidationError{Err: fmt.Errorf("idempotency_key: %w", err)}
	}
	run, err := service.base.GetOperationRun(ctx, id)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if run.Spec.OperationID != sysctlrepair.ID {
		return operationrun.Resource{}, &ConflictError{Err: ErrProductApplyDisabled}
	}
	if run.PlanDigest == "" || strings.TrimSpace(request.PlanDigest) != run.PlanDigest {
		return operationrun.Resource{}, &ConflictError{Err: ErrOperationPlanDigestConflict}
	}
	for {
		switch run.Status.State {
		case operation.StateAwaitingConfirm:
			if _, err := service.ensureLease(ctx, run); err != nil {
				return operationrun.Resource{}, err
			}
			run, err = service.advance(ctx, run, operation.StateQueued, "confirmed_and_queued", "operation confirmed; authoritative lease acquired", nil)
		case operation.StateQueued:
			if _, err := service.ensureLease(ctx, run); err != nil {
				return operationrun.Resource{}, err
			}
			run, err = service.advance(ctx, run, operation.StateAcquiringLock, "lease_acquired", "authoritative operation lease is supervised", nil)
		case operation.StateAcquiringLock:
			if _, err := service.ensureLease(ctx, run); err != nil {
				return operationrun.Resource{}, err
			}
			return service.queueAction(ctx, run, run.Status.TaskID, task.OperationActionCreateRestorePoint, operation.StateCreatingRestorePoint, "create_restore_point_queued")
		case operation.StateCreatingRestorePoint, operation.StateRunning, operation.StateVerifying, operation.StateRollingBack:
			return run, nil
		default:
			return operationrun.Resource{}, &ConflictError{Err: ErrOperationStateConflict}
		}
		if err != nil {
			return operationrun.Resource{}, err
		}
	}
}

func (service *ProductOperations) CancelOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	run, err := service.base.CancelOperationRun(ctx, id)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if run.Status.State == operation.StateCanceledBeforeApply {
		_ = service.releaseLease(ctx, run)
	}
	return run, nil
}

// ContinueOperationRun is called only after the bounded action result is
// durable. It is the Server-owned decision point for at most one next action.
func (service *ProductOperations) ContinueOperationRun(ctx context.Context, runID string) error {
	run, err := service.runs.GetOperationRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Spec.OperationID != sysctlrepair.ID {
		return nil
	}
	if operation.Terminal(run.Status.State) {
		_ = service.releaseLease(ctx, run)
		return nil
	}
	resource, err := service.runs.GetTask(ctx, run.Status.TaskID)
	if err != nil {
		return err
	}
	if resource.Kind != task.KindOperationExecutionTask || resource.Spec.OperationExecution == nil {
		if strings.HasSuffix(run.Status.Checkpoint, "_queued") {
			return nil
		}
		return errors.New("operation continuation requires the durable bounded action task")
	}
	if !task.Terminal(resource.Status.Phase) {
		return nil
	}
	action := resource.Spec.OperationExecution.Action
	if !strings.HasPrefix(run.Status.Checkpoint, "action_"+string(action)+"_") {
		return nil
	}

	if resource.Status.Phase != task.PhaseSucceeded {
		switch action {
		case task.OperationActionCreateRestorePoint:
			_, err = service.advance(ctx, run, operation.StateBlocked, "restore_point_failed", "restore point creation failed before mutation", &operationrun.Recovery{Code: "restore_point_failed", SafeNext: "inspect_and_create_new_run", ManualReview: true})
			if err == nil {
				err = service.releaseLease(ctx, run)
			}
			return err
		case task.OperationActionApply:
			_, err = service.advance(ctx, run, operation.StateInterrupted, "apply_outcome_requires_reconcile", "Apply failed with mutation outcome requiring reconciliation", &operationrun.Recovery{Code: "apply_outcome_uncertain", Checkpoint: run.Status.Checkpoint, SafeNext: "reconcile_before_retry_or_rollback", ManualReview: true})
			return err
		case task.OperationActionVerify:
			if run.Execution == nil || run.Execution.RestorePoint == nil || run.Execution.Apply == nil {
				_, err = service.advance(ctx, run, operation.StateInterrupted, "verify_failed_without_rollback_proof", "verification failed without complete rollback prerequisites", &operationrun.Recovery{Code: "rollback_prerequisites_missing", SafeNext: "reconcile", ManualReview: true})
				return err
			}
			_, err = service.ensureLease(ctx, run)
			if err != nil {
				return err
			}
			_, err = service.queueAction(ctx, run, resource.Metadata.ID, task.OperationActionRollback, operation.StateRollingBack, "rollback_queued")
			return err
		case task.OperationActionRollback, task.OperationActionVerifyRollback:
			_, err = service.advance(ctx, run, operation.StateRollbackFailed, "rollback_failed", "rollback or rollback verification failed", &operationrun.Recovery{Code: "rollback_failed", SafeNext: "manual_recovery", ManualReview: true})
			if err == nil {
				err = service.releaseLease(ctx, run)
			}
			return err
		}
	}

	switch action {
	case task.OperationActionCreateRestorePoint:
		_, err = service.queueAction(ctx, run, resource.Metadata.ID, task.OperationActionApply, operation.StateRunning, "apply_queued")
	case task.OperationActionApply:
		_, err = service.queueAction(ctx, run, resource.Metadata.ID, task.OperationActionVerify, operation.StateVerifying, "verify_queued")
	case task.OperationActionVerify:
		_, err = service.advance(ctx, run, operation.StateSucceeded, "verified", "operation verification passed", nil)
		if err == nil {
			err = service.releaseLease(ctx, run)
		}
	case task.OperationActionRollback:
		_, err = service.queueAction(ctx, run, resource.Metadata.ID, task.OperationActionVerifyRollback, operation.StateRollingBack, "verify_rollback_queued")
	case task.OperationActionVerifyRollback:
		_, err = service.advance(ctx, run, operation.StateRolledBack, "rollback_verified", "rollback verification passed", nil)
		if err == nil {
			err = service.releaseLease(ctx, run)
		}
	default:
		err = fmt.Errorf("unsupported operation continuation action %q", action)
	}
	return err
}

func (service *ProductOperations) queueAction(ctx context.Context, run operationrun.Resource, completedTaskID string, action task.OperationAction, state operation.State, checkpoint string) (operationrun.Resource, error) {
	if run.Plan == nil {
		return operationrun.Resource{}, errors.New("operation run has no durable plan")
	}
	contract := task.OperationExecutionContract{
		OperationID: run.Spec.OperationID, RunID: run.Metadata.ID, Action: action, PlanDigest: run.PlanDigest,
		Targets: append([]operation.Target(nil), run.Spec.Targets...), Plan: *run.Plan,
	}
	switch action {
	case task.OperationActionApply:
		if run.Impact == nil || run.Execution == nil || run.Execution.RestorePoint == nil {
			return operationrun.Resource{}, errors.New("Apply continuation requires impact and restore point")
		}
		contract.Impact = run.Impact
		contract.RestorePoint = run.Execution.RestorePoint
	case task.OperationActionVerify:
		if run.Execution == nil || run.Execution.Apply == nil {
			return operationrun.Resource{}, errors.New("verify continuation requires Apply result")
		}
		contract.Apply = run.Execution.Apply
	case task.OperationActionRollback:
		if run.Execution == nil || run.Execution.RestorePoint == nil || run.Execution.Apply == nil {
			return operationrun.Resource{}, errors.New("rollback continuation requires restore point and Apply result")
		}
		contract.RestorePoint = run.Execution.RestorePoint
		contract.Apply = run.Execution.Apply
	case task.OperationActionVerifyRollback:
		if run.Execution == nil || run.Execution.RestorePoint == nil || run.Execution.Rollback == nil {
			return operationrun.Resource{}, errors.New("verify rollback continuation requires restore point and rollback result")
		}
		contract.RestorePoint = run.Execution.RestorePoint
		contract.Rollback = run.Execution.Rollback
	}
	frozen, digest, err := task.NewOperationExecutionContract(contract)
	if err != nil {
		return operationrun.Resource{}, err
	}
	at := service.now().UTC()
	taskID := run.Metadata.ID + ":" + string(action)
	next := task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindOperationExecutionTask,
		Metadata: task.Metadata{ID: taskID, IdempotencyKey: taskID, CreatedAt: at},
		Spec: task.Spec{
			NodeID: run.Spec.NodeID, OperationExecution: &frozen, ContractDigest: digest,
			OperationID: run.Spec.OperationID, OperationVersion: run.Spec.OperationVersion, CapabilityDigest: run.Spec.CapabilityDigest,
			Targets: append([]operation.Target(nil), run.Spec.Targets...), Parameters: append([]byte(nil), run.Spec.Parameters...), SecretRefs: append([]operation.SecretRef(nil), run.Spec.SecretRefs...),
		},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: at},
	}
	sequence, err := service.nextJournalSequence(ctx, run.Metadata.ID)
	if err != nil {
		return operationrun.Resource{}, err
	}
	journal := operation.JournalEntry{RunID: run.Metadata.ID, Sequence: sequence, State: state, Checkpoint: checkpoint, Message: "Server queued bounded action " + string(action), At: at,
		Evidence: []operation.EvidenceRef{{ID: taskID, Kind: "operation_action_task"}, {ID: string(action), Kind: "operation_action"}}}
	return service.runs.ContinueOperationRun(ctx, run.Metadata.ID, completedTaskID, state, checkpoint, next, journal, at)
}

func (service *ProductOperations) advance(ctx context.Context, run operationrun.Resource, state operation.State, checkpoint, message string, recovery *operationrun.Recovery) (operationrun.Resource, error) {
	sequence, err := service.nextJournalSequence(ctx, run.Metadata.ID)
	if err != nil {
		return operationrun.Resource{}, err
	}
	at := service.now().UTC()
	journal := operation.JournalEntry{RunID: run.Metadata.ID, Sequence: sequence, State: state, Checkpoint: checkpoint, Message: message, At: at}
	return service.runs.SaveOperationExecutionCheckpoint(ctx, run.Metadata.ID, state, checkpoint, operationrun.ExecutionSnapshot{}, recovery, journal, at)
}

func (service *ProductOperations) nextJournalSequence(ctx context.Context, runID string) (int64, error) {
	entries, err := service.runs.List(ctx, runID)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 1, nil
	}
	return entries[len(entries)-1].Sequence + 1, nil
}

func (service *ProductOperations) ensureLease(ctx context.Context, run operationrun.Resource) (operation.LockLease, error) {
	if lease, found, err := service.lease.CurrentLeaseByOwner(ctx, run.Metadata.ID); err == nil && found {
		return lease, nil
	}
	if lease, err := service.lease.Resume(ctx, run.Metadata.ID, run.Spec.Targets); err == nil {
		return lease, nil
	}
	return service.lease.Acquire(ctx, run.Metadata.ID, run.Spec.Targets)
}

func (service *ProductOperations) releaseLease(ctx context.Context, run operationrun.Resource) error {
	if err := service.lease.Release(ctx, run.Metadata.ID); err != nil && !errors.Is(err, operation.ErrLeaseAuthorityUnavailable) {
		return err
	}
	return nil
}

type operationContinuation interface {
	ContinueOperationRun(context.Context, string) error
}

type ProductService struct {
	*Service
	continuation operationContinuation
}

func NewProductService(base *Service, continuation operationContinuation) (*ProductService, error) {
	if base == nil || continuation == nil {
		return nil, errors.New("base service and operation continuation are required")
	}
	return &ProductService{Service: base, continuation: continuation}, nil
}

func (service *ProductService) SubmitTaskResult(ctx context.Context, agentID, taskID string, submission task.ResultSubmission) (task.Resource, error) {
	existing, err := service.nodes.GetTask(ctx, strings.TrimSpace(taskID))
	if err != nil || existing.Kind != task.KindOperationExecutionTask {
		return service.Service.SubmitTaskResult(ctx, agentID, taskID, submission)
	}
	if err := validateTaskAgentRequest(agentID, taskID, submission.ClaimID); err != nil {
		return task.Resource{}, &ValidationError{Err: err}
	}
	if existing.Spec.OperationExecution == nil || submission.OperationExecutionResult == nil || submission.Result != nil || submission.OperationResult != nil {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution task requires exactly one bounded action result")}
	}
	if !task.ValidResultPhase(submission.Phase) {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution result phase must be terminal")}
	}
	result := submission.OperationExecutionResult
	contract := existing.Spec.OperationExecution
	if result.OperationID != contract.OperationID || result.RunID != contract.RunID || result.Action != contract.Action {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution result does not match the frozen bounded action contract")}
	}
	resource, err := service.nodes.CompleteTask(ctx, strings.TrimSpace(agentID), strings.TrimSpace(taskID), submission, service.now().UTC())
	if err != nil {
		return task.Resource{}, classifyTaskConflict(err)
	}
	if err := service.continuation.ContinueOperationRun(ctx, contract.RunID); err != nil {
		return resource, err
	}
	return resource, nil
}
