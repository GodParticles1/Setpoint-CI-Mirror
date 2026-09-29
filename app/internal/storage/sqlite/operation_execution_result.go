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
	if result.RestorePoint != nil && (result.RestorePoint.OperationID != contract.OperationID || result.RestorePoint.RunID != contract.RunID || !reflect.DeepEqual(result.RestorePoint.Targets, contract.Targets)) {
		return preparedOperationExecutionResult{}, errors.New("restore point result does not match its exact participant stage")
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
	if len(contract.ParticipantNodeIDs) > 0 && (result.StageID != contract.Stage.ID || result.StageIndex != contract.StageIndex || result.ExecutorNodeID != contract.Stage.ExecutorNodeID) {
		return preparedOperationExecutionResult{}, errors.New("operation execution result stage does not match the frozen task contract")
	}
	if len(contract.ParticipantNodeIDs) > 0 && !reflect.DeepEqual(result.ParticipantNodeIDs, contract.ParticipantNodeIDs) {
		return preparedOperationExecutionResult{}, errors.New("operation execution result participants do not match the frozen canonical participant set")
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
	if current.Status.State == operation.StateCanceledBeforeApply {
		return prepareCanceledOperationExecutionResult(resource, *contract, submission.Phase, *result, sequence, at)
	}
	expectedState, err := operationActionState(contract.Action)
	if err != nil {
		return preparedOperationExecutionResult{}, err
	}
	if current.Status.State != expectedState {
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: stale %s result for operation state %s", task.ErrInvalidTransition, contract.Action, current.Status.State)
	}

	snapshot := operationExecutionResultSnapshot(*contract, submission.Phase, *result, at)
	checkpoint := operationContractResultCheckpoint(*contract, submission.Phase)
	journal := operation.JournalEntry{
		RunID:      contract.RunID,
		Sequence:   sequence + 1,
		State:      current.Status.State,
		Checkpoint: checkpoint,
		Message:    fmt.Sprintf("operation action %s task %s reported %s", contract.Action, resource.Metadata.ID, submission.Phase),
		At:         at,
		Evidence:   operationActionJournalEvidence(resource.Metadata.ID, *contract, submission.Phase, result.Error),
	}
	if err := operation.ValidateJournalEntry(journal); err != nil {
		return preparedOperationExecutionResult{}, err
	}
	return preparedOperationExecutionResult{
		runID: contract.RunID, state: current.Status.State, checkpoint: checkpoint,
		snapshot: snapshot, journal: journal,
	}, nil
}

func prepareCanceledOperationExecutionResult(
	resource task.Resource,
	contract task.OperationExecutionContract,
	phase task.Phase,
	result task.OperationExecutionResult,
	sequence int64,
	at time.Time,
) (preparedOperationExecutionResult, error) {
	if resource.Status.Phase != task.PhaseCancelRequested || contract.Action != task.OperationActionCreateRestorePoint {
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: canceled operation run has no matching live pre-Apply task", task.ErrInvalidTransition)
	}
	checkpoint := "cancellation_converged"
	message := "bounded pre-Apply task cancellation acknowledged"
	snapshot := operationrun.ExecutionSnapshot{}
	switch phase {
	case task.PhaseSucceeded:
		checkpoint = "canceled_before_apply_restore_point_retained"
		message = "bounded RestorePoint completed before cancellation was observed; evidence retained without Apply"
		snapshot = operationExecutionResultSnapshot(contract, phase, result, at)
	case task.PhaseFailed:
		checkpoint = "canceled_before_apply_failure_reconciled"
		message = "bounded pre-Apply task failed while cancellation converged"
	case task.PhaseCanceled:
	default:
		return preparedOperationExecutionResult{}, fmt.Errorf("%w: cancellation convergence requires a terminal bounded task result", task.ErrInvalidTransition)
	}
	journal := operation.JournalEntry{
		RunID: contract.RunID, Sequence: sequence + 1, State: operation.StateCanceledBeforeApply,
		Checkpoint: checkpoint, Message: message, At: at,
		Evidence: operationActionJournalEvidence(resource.Metadata.ID, contract, phase, result.Error),
	}
	if err := operation.ValidateJournalEntry(journal); err != nil {
		return preparedOperationExecutionResult{}, err
	}
	return preparedOperationExecutionResult{
		runID: contract.RunID, state: operation.StateCanceledBeforeApply, checkpoint: checkpoint,
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
	if len(contract.ParticipantNodeIDs) > 0 {
		if !reflect.DeepEqual(run.Spec.ParticipantNodeIDs, contract.ParticipantNodeIDs) || resource.Spec.NodeID != contract.Stage.ExecutorNodeID {
			return errors.New("operation execution task participant or executor does not match the authoritative operation run")
		}
	} else if run.Spec.NodeID != resource.Spec.NodeID {
		return errors.New("operation execution task node does not match the authoritative operation run")
	}
	expectedTargets := operationRunExecutionTargets(run)
	if len(contract.ParticipantNodeIDs) > 0 {
		expectedTargets = operationrun.StageTargets(run, contract.Stage)
	}
	if !reflect.DeepEqual(contract.Targets, resource.Spec.Targets) || !reflect.DeepEqual(expectedTargets, resource.Spec.Targets) {
		return errors.New("operation execution task targets do not match the authoritative operation run")
	}
	if string(run.Spec.Parameters) != string(resource.Spec.Parameters) || !equalSecretRefs(run.Spec.SecretRefs, resource.Spec.SecretRefs) {
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
	facts, factsErr := persistedStageFacts(run, contract.StageIndex)
	if factsErr != nil && (contract.RestorePoint != nil || contract.Apply != nil || contract.Rollback != nil) {
		return factsErr
	}
	if contract.RestorePoint != nil && (facts.RestorePoint == nil || !reflect.DeepEqual(*facts.RestorePoint, *contract.RestorePoint)) {
		return errors.New("operation execution task restore point does not match the authoritative operation run")
	}
	if contract.Apply != nil && (facts.Apply == nil || !reflect.DeepEqual(*facts.Apply, *contract.Apply)) {
		return errors.New("operation execution task apply input does not match the authoritative operation run")
	}
	if contract.Rollback != nil && (facts.Rollback == nil || !reflect.DeepEqual(*facts.Rollback, *contract.Rollback)) {
		return errors.New("operation execution task rollback input does not match the authoritative operation run")
	}
	return nil
}

func persistedStageFacts(run operationrun.Resource, stageIndex int) (operationrun.StageExecutionSnapshot, error) {
	if run.Execution != nil {
		for _, stage := range run.Execution.Stages {
			if stage.StageIndex == stageIndex {
				return stage, nil
			}
		}
	}
	if len(run.Spec.ParticipantNodeIDs) <= 1 && run.Execution != nil {
		return operationrun.StageExecutionSnapshot{StageIndex: 0, StageID: operationrun.SingleNodeStageID, ExecutorNodeID: run.Spec.NodeID,
			RestorePoint: run.Execution.RestorePoint, Apply: run.Execution.Apply, Verification: run.Execution.Verification,
			Rollback: run.Execution.Rollback, RollbackVerification: run.Execution.RollbackVerification}, nil
	}
	return operationrun.StageExecutionSnapshot{}, errors.New("operation execution stage facts are missing")
}

func equalSecretRefs(left, right []operation.SecretRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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

func operationExecutionResultSnapshot(contract task.OperationExecutionContract, phase task.Phase, result task.OperationExecutionResult, at time.Time) operationrun.ExecutionSnapshot {
	action := contract.Action
	stage := operationrun.StageExecutionSnapshot{StageIndex: contract.StageIndex, StageID: contract.Stage.ID, ExecutorNodeID: contract.Stage.ExecutorNodeID}
	staged := len(contract.ParticipantNodeIDs) > 0
	multiNode := len(contract.ParticipantNodeIDs) > 1
	if phase == task.PhaseFailed {
		switch action {
		case task.OperationActionApply:
			if result.Apply != nil {
				stage.ApplyAt = at
				stage.Apply = result.Apply
				return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{Apply: result.Apply})
			}
		case task.OperationActionVerify:
			if result.Verification != nil && !result.Verification.Passed {
				stage.VerificationAt = at
				stage.Verification = result.Verification
				return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{Verification: result.Verification})
			}
		case task.OperationActionVerifyRollback:
			if result.Verification != nil && !result.Verification.Passed {
				stage.RollbackVerificationAt = at
				stage.RollbackVerification = result.Verification
				return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{RollbackVerification: result.Verification})
			}
		}
	}
	if phase != task.PhaseSucceeded {
		return operationrun.ExecutionSnapshot{}
	}
	switch action {
	case task.OperationActionCreateRestorePoint:
		stage.RestorePointAt = at
		stage.RestorePoint = result.RestorePoint
		return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{RestorePoint: result.RestorePoint})
	case task.OperationActionApply:
		stage.ApplyAt = at
		stage.Apply = result.Apply
		return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{Apply: result.Apply})
	case task.OperationActionVerify:
		stage.VerificationAt = at
		stage.Verification = result.Verification
		return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{Verification: result.Verification})
	case task.OperationActionRollback:
		stage.RollbackAt = at
		stage.Rollback = result.Rollback
		return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{Rollback: result.Rollback})
	case task.OperationActionVerifyRollback:
		stage.RollbackVerificationAt = at
		stage.RollbackVerification = result.Verification
		return executionResultSnapshot(stage, staged, multiNode, operationrun.ExecutionSnapshot{RollbackVerification: result.Verification})
	default:
		return operationrun.ExecutionSnapshot{}
	}
}

func executionResultSnapshot(stage operationrun.StageExecutionSnapshot, staged, multiNode bool, scalar operationrun.ExecutionSnapshot) operationrun.ExecutionSnapshot {
	if multiNode {
		scalar = operationrun.ExecutionSnapshot{}
	}
	if staged {
		scalar.Stages = []operationrun.StageExecutionSnapshot{stage}
	}
	return scalar
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

func operationContractResultCheckpoint(contract task.OperationExecutionContract, phase task.Phase) string {
	checkpoint := operationActionResultCheckpoint(contract.Action, phase)
	if len(contract.ParticipantNodeIDs) > 1 {
		return fmt.Sprintf("stage_%d_%s", contract.StageIndex, checkpoint)
	}
	return checkpoint
}

func operationActionJournalEvidence(taskID string, contract task.OperationExecutionContract, phase task.Phase, failure *task.Failure) []operation.EvidenceRef {
	evidence := []operation.EvidenceRef{
		{ID: taskID, Kind: "operation_action_task"},
		{ID: string(contract.Action), Kind: "operation_action"},
		{ID: string(phase), Kind: "task_result_phase"},
	}
	if len(contract.ParticipantNodeIDs) > 0 {
		evidence = append(evidence,
			operation.EvidenceRef{ID: contract.Stage.ID, Kind: "operation_stage"},
			operation.EvidenceRef{ID: contract.Stage.ExecutorNodeID, Kind: "operation_stage_executor"},
		)
	}
	if failure != nil {
		evidence = append(evidence, operation.EvidenceRef{ID: failure.Code, Kind: "operation_action_error_code"})
	}
	return evidence
}
