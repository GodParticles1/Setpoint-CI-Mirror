package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

type preparedOperationExecutionResult struct {
	runID      string
	state      operation.State
	checkpoint string
	snapshot   operationrun.ExecutionSnapshot
	journal    operation.JournalEntry
}

func prepareOperationExecutionResultTx(
	ctx context.Context,
	transaction *sql.Tx,
	resource task.Resource,
	submission task.ResultSubmission,
	at time.Time,
) (preparedOperationExecutionResult, error) {
	if resource.Kind != task.KindOperationExecutionTask || resource.Spec.OperationExecution == nil || submission.OperationExecutionResult == nil {
		return preparedOperationExecutionResult{}, errors.New("operation execution task requires a frozen contract and execution result")
	}
	contract := resource.Spec.OperationExecution
	result := submission.OperationExecutionResult
	if err := task.ValidateOperationExecutionContract(*contract, resource.Spec.ContractDigest); err != nil {
		return preparedOperationExecutionResult{}, fmt.Errorf("validate operation execution contract before result persistence: %w", err)
	}
	if err := validateOperationExecutionResultShape(contract.Action, submission.Phase, *result); err != nil {
		return preparedOperationExecutionResult{}, err
	}
	if result.OperationID != contract.OperationID || result.OperationID != resource.Spec.OperationID {
		return preparedOperationExecutionResult{}, errors.New("operation execution result operation ID does not match the frozen task contract")
	}
	if result.RunID != contract.RunID {
		return preparedOperationExecutionResult{}, errors.New("operation execution result run ID does not match the frozen task contract")
	}
	if result.Action != contract.Action {
		return preparedOperationExecutionResult{}, errors.New("operation execution result action does not match the frozen task contract")
	}

	current, err := scanOperationRun(transaction.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE id = ?`, contract.RunID))
	if errors.Is(err, sql.ErrNoRows) {
		return preparedOperationExecutionResult{}, domain.ErrNotFound
	}
	if err != nil {
		return preparedOperationExecutionResult{}, err
	}
	if current.Status.TaskID != resource.Metadata.ID {
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: operation run is bound to task %s, not completing task %s", task.ErrInvalidTransition, current.Status.TaskID, resource.Metadata.ID)
	}
	if err := validateOperationExecutionTaskRunCorrelation(resource, *contract, current); err != nil {
		return preparedOperationExecutionResult{}, err
	}
	expectedState, err := operationActionState(contract.Action)
	if err != nil {
		return preparedOperationExecutionResult{}, err
	}
	if current.Status.State != expectedState {
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: stale %s result for operation state %s", task.ErrInvalidTransition, contract.Action, current.Status.State)
	}

	var sequence int64
	var journalState, journalCheckpoint string
	err = transaction.QueryRowContext(ctx, `SELECT sequence, state, checkpoint FROM operation_journal WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`, contract.RunID).
		Scan(&sequence, &journalState, &journalCheckpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: operation action state %s has no durable journal authority", task.ErrInvalidTransition, current.Status.State)
	}
	if err != nil {
		return preparedOperationExecutionResult{}, fmt.Errorf("read operation journal tail before action result persistence: %w", err)
	}
	if operation.State(journalState) != current.Status.State || journalCheckpoint != current.Status.Checkpoint {
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: operation run state/checkpoint diverges from journal tail", task.ErrInvalidTransition)
	}

	snapshot := operationExecutionResultSnapshot(contract.Action, submission.Phase, *result)
	checkpoint := operationActionResultCheckpoint(contract.Action, submission.Phase)
	journal := operation.JournalEntry{
		RunID:      contract.RunID,
		Sequence:   sequence + 1,
		State:      current.Status.State,
		Checkpoint: checkpoint,
		Message:    fmt.Sprintf("operation action %s task %s reported %s", contract.Action, resource.Metadata.ID, submission.Phase),
		At:         at,
		Evidence:   operationActionJournalEvidence(resource.Metadata.ID, contract.Action, submission.Phase, result.Error),
	}
	if err := operation.ValidateJournalEntry(journal); err != nil {
		return preparedOperationExecutionResult{}, err
	}
	return preparedOperationExecutionResult{
		runID: contract.RunID, state: current.Status.State, checkpoint: checkpoint,
		snapshot: snapshot, journal: journal,
	}, nil
}

func validateOperationExecutionTaskRunCorrelation(resource task.Resource, contract task.OperationExecutionContract, run operationrun.Resource) error {
	if run.Metadata.ID != contract.RunID || run.Spec.OperationID != contract.OperationID || run.Spec.OperationID != resource.Spec.OperationID {
		return errors.New("operation execution task does not match the authoritative operation run identity")
	}
	if run.Spec.OperationVersion != resource.Spec.OperationVersion {
		return errors.New("operation execution task version does not match the authoritative operation run")
	}
	if run.Spec.CapabilityDigest != resource.Spec.CapabilityDigest {
		return errors.New("operation execution task capability digest does not match the authoritative operation run")
	}
	if run.Spec.NodeID != resource.Spec.NodeID {
		return errors.New("operation execution task node does not match the authoritative operation run")
	}
	expectedTargets := operationRunExecutionTargets(run)
	if !reflect.DeepEqual(contract.Targets, resource.Spec.Targets) || !reflect.DeepEqual(expectedTargets, resource.Spec.Targets) {
		return errors.New("operation execution task targets do not match the authoritative operation run")
	}
	if string(run.Spec.Parameters) != string(resource.Spec.Parameters) || !reflect.DeepEqual(run.Spec.SecretRefs, resource.Spec.SecretRefs) {
		return errors.New("operation execution task inputs do not match the authoritative operation run")
	}
	if run.PlanDigest != contract.PlanDigest {
		return errors.New("operation execution task plan digest does not match the authoritative operation run")
	}
	if run.Plan == nil || !reflect.DeepEqual(*run.Plan, contract.Plan) {
		return errors.New("operation execution task plan does not match the authoritative operation run")
	}
	if contract.Impact != nil && (run.Impact == nil || !reflect.DeepEqual(*run.Impact, *contract.Impact)) {
		return errors.New("operation execution task impact does not match the authoritative operation run")
	}
	if contract.RestorePoint != nil && (run.Execution == nil || run.Execution.RestorePoint == nil || !reflect.DeepEqual(*run.Execution.RestorePoint, *contract.RestorePoint)) {
		return errors.New("operation execution task restore point does not match the authoritative operation run")
	}
	if contract.Apply != nil && (run.Execution == nil || run.Execution.Apply == nil || !reflect.DeepEqual(*run.Execution.Apply, *contract.Apply)) {
		return errors.New("operation execution task apply input does not match the authoritative operation run")
	}
	if contract.Rollback != nil && (run.Execution == nil || run.Execution.Rollback == nil || !reflect.DeepEqual(*run.Execution.Rollback, *contract.Rollback)) {
		return errors.New("operation execution task rollback input does not match the authoritative operation run")
	}
	return nil
}

func operationRunExecutionTargets(run operationrun.Resource) []operation.Target {
	targets := make([]operation.Target, 0, len(run.Spec.Targets))
	seen := make(map[operation.Target]struct{})
	appendTarget := func(target operation.Target) {
		if _, exists := seen[target]; exists {
			return
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	for _, target := range run.Spec.Targets {
		appendTarget(target)
	}
	if run.Plan != nil {
		for _, step := range run.Plan.Steps {
			appendTarget(step.Target)
		}
	}
	return targets
}

func validateOperationExecutionResultShape(action task.OperationAction, phase task.Phase, result task.OperationExecutionResult) error {
	if !task.ValidResultPhase(phase) {
		return errors.New("operation execution result phase must be succeeded, failed, or canceled")
	}
	outputs := 0
	if result.RestorePoint != nil {
		outputs++
	}
	if result.Apply != nil {
		outputs++
	}
	if result.Verification != nil {
		outputs++
	}
	if result.Rollback != nil {
		outputs++
	}
	if phase == task.PhaseCanceled {
		if result.Error == nil || strings.TrimSpace(result.Error.Code) == "" || strings.TrimSpace(result.Error.Message) == "" {
			return errors.New("failed or canceled operation action requires a typed error")
		}
		if outputs != 0 {
			return errors.New("canceled operation action must not carry action output")
		}
		return nil
	}
	if phase == task.PhaseFailed {
		if result.Error == nil || strings.TrimSpace(result.Error.Code) == "" || strings.TrimSpace(result.Error.Message) == "" {
			return errors.New("failed or canceled operation action requires a typed error")
		}
		switch action {
		case task.OperationActionVerify, task.OperationActionVerifyRollback:
			if result.RestorePoint != nil || result.Apply != nil || result.Rollback != nil {
				return errors.New("failed verification action must not carry mutation output")
			}
			if result.Verification != nil && result.Verification.Passed {
				return errors.New("failed verification action must not carry positive verification evidence")
			}
			return nil
		case task.OperationActionApply:
			if result.RestorePoint != nil || result.Verification != nil || result.Rollback != nil {
				return errors.New("failed apply action must not carry mixed action output")
			}
			if result.Apply != nil {
				if err := validateFailedApplyEvidence(*result.Apply); err != nil {
					return err
				}
			}
			return nil
		case task.OperationActionCreateRestorePoint, task.OperationActionRollback:
			if outputs != 0 {
				return errors.New("failed mutation action must not carry optimistic success output")
			}
			return nil
		default:
			return fmt.Errorf("unsupported operation action %q", action)
		}
	}
	if result.Error != nil {
		return errors.New("succeeded operation action must not contain an error")
	}
	if outputs != 1 {
		return errors.New("succeeded operation action requires exactly one action-local output")
	}
	switch action {
	case task.OperationActionCreateRestorePoint:
		if result.RestorePoint == nil {
			return errors.New("create_restore_point success requires only a restore point")
		}
	case task.OperationActionApply:
		if result.Apply == nil {
			return errors.New("apply success requires only an apply result")
		}
	case task.OperationActionVerify, task.OperationActionVerifyRollback:
		if result.Verification == nil {
			return errors.New("verification success requires only a verification result")
		}
		if !result.Verification.Passed {
			return errors.New("succeeded verification action requires positive verification evidence")
		}
	case task.OperationActionRollback:
		if result.Rollback == nil {
			return errors.New("rollback success requires only a rollback result")
		}
	default:
		return fmt.Errorf("unsupported operation action %q", action)
	}
	return nil
}

func validateFailedApplyEvidence(result operation.ApplyResult) error {
	if strings.TrimSpace(result.Checkpoint) == "" {
		return errors.New("failed apply evidence requires a checkpoint")
	}
	if strings.TrimSpace(result.State.SchemaVersion) == "" {
		return errors.New("failed apply evidence requires a state schema version")
	}
	if len(result.State.Payload) == 0 {
		return errors.New("failed apply evidence requires state payload")
	}
	if !json.Valid(result.State.Payload) {
		return errors.New("failed apply evidence state payload must be valid JSON")
	}
	return nil
}

func operationExecutionResultSnapshot(action task.OperationAction, phase task.Phase, result task.OperationExecutionResult) operationrun.ExecutionSnapshot {
	if phase == task.PhaseFailed {
		switch action {
		case task.OperationActionApply:
			if result.Apply != nil {
				return operationrun.ExecutionSnapshot{Apply: result.Apply}
			}
		case task.OperationActionVerify:
			if result.Verification != nil && !result.Verification.Passed {
				return operationrun.ExecutionSnapshot{Verification: result.Verification}
			}
		case task.OperationActionVerifyRollback:
			if result.Verification != nil && !result.Verification.Passed {
				return operationrun.ExecutionSnapshot{RollbackVerification: result.Verification}
			}
		}
	}
	if phase != task.PhaseSucceeded {
		return operationrun.ExecutionSnapshot{}
	}
	switch action {
	case task.OperationActionCreateRestorePoint:
		return operationrun.ExecutionSnapshot{RestorePoint: result.RestorePoint}
	case task.OperationActionApply:
		return operationrun.ExecutionSnapshot{Apply: result.Apply}
	case task.OperationActionVerify:
		return operationrun.ExecutionSnapshot{Verification: result.Verification}
	case task.OperationActionRollback:
		return operationrun.ExecutionSnapshot{Rollback: result.Rollback}
	case task.OperationActionVerifyRollback:
		return operationrun.ExecutionSnapshot{RollbackVerification: result.Verification}
	default:
		return operationrun.ExecutionSnapshot{}
	}
}

func operationActionState(action task.OperationAction) (operation.State, error) {
	switch action {
	case task.OperationActionCreateRestorePoint:
		return operation.StateCreatingRestorePoint, nil
	case task.OperationActionApply:
		return operation.StateRunning, nil
	case task.OperationActionVerify:
		return operation.StateVerifying, nil
	case task.OperationActionRollback, task.OperationActionVerifyRollback:
		return operation.StateRollingBack, nil
	default:
		return "", fmt.Errorf("unsupported operation action %q", action)
	}
}

func operationActionResultCheckpoint(action task.OperationAction, phase task.Phase) string {
	return "action_" + string(action) + "_" + string(phase)
}

func operationActionJournalEvidence(taskID string, action task.OperationAction, phase task.Phase, failure *task.Failure) []operation.EvidenceRef {
	evidence := []operation.EvidenceRef{
		{ID: taskID, Kind: "operation_action_task"},
		{ID: string(action), Kind: "operation_action"},
		{ID: string(phase), Kind: "task_result_phase"},
	}
	if failure != nil {
		evidence = append(evidence, operation.EvidenceRef{ID: failure.Code, Kind: "operation_action_error_code"})
	}
	return evidence
}
