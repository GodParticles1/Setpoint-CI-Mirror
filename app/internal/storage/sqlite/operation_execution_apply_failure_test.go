package sqlite

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestFailedApplyEvidenceIsDurableAtomicAndIdempotent(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionApply)
	defer fixture.store.Close()

	beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)
	apply := validFailedApplyEvidence()
	fixture.submission.Phase = task.PhaseFailed
	fixture.submission.OperationExecutionResult.Apply = &apply
	fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "partial mutation failed"}

	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseFailed || completed.Status.LastError == nil || completed.Status.LastError.Code != "apply_failed" {
		t.Fatalf("completed=%#v", completed)
	}
	if completed.OperationExecutionResult == nil || completed.OperationExecutionResult.Apply == nil || !reflect.DeepEqual(*completed.OperationExecutionResult.Apply, apply) {
		t.Fatalf("completed result=%#v", completed.OperationExecutionResult)
	}
	afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.Status.State != operation.StateRunning {
		t.Fatalf("failed Apply evidence advanced state=%s", afterRun.Status.State)
	}
	if afterRun.Status.Checkpoint != operationActionResultCheckpoint(task.OperationActionApply, task.PhaseFailed) {
		t.Fatalf("checkpoint=%s", afterRun.Status.Checkpoint)
	}
	if afterRun.Execution == nil || afterRun.Execution.Apply == nil || !reflect.DeepEqual(*afterRun.Execution.Apply, apply) {
		t.Fatalf("durable failed Apply evidence=%#v", afterRun.Execution)
	}
	if beforeRun.Execution == nil || beforeRun.Execution.RestorePoint == nil || !reflect.DeepEqual(afterRun.Execution.RestorePoint, beforeRun.Execution.RestorePoint) {
		t.Fatalf("restore point changed: before=%#v after=%#v", beforeRun.Execution, afterRun.Execution)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal+1 {
		t.Fatalf("journal before=%d after=%d", beforeJournal, got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks {
		t.Fatalf("next action task created: before=%d after=%d", beforeTasks, got)
	}

	journalCount := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt.Add(time.Minute)); err != nil {
		t.Fatalf("identical failed Apply retry: %v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
		t.Fatalf("identical retry appended journal: before=%d after=%d", journalCount, got)
	}

	conflicting := fixture.submission
	resultCopy := *fixture.submission.OperationExecutionResult
	applyCopy := *resultCopy.Apply
	applyCopy.Checkpoint = "different_partial_fact"
	resultCopy.Apply = &applyCopy
	conflicting.OperationExecutionResult = &resultCopy
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, conflicting, fixture.reportedAt.Add(2*time.Minute)); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("conflicting failed Apply retry error=%v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
		t.Fatalf("conflicting retry appended journal: before=%d after=%d", journalCount, got)
	}
}

func TestFailedApplyWithoutEvidenceIsAcceptedFailClosed(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionApply)
	defer fixture.store.Close()
	before, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.submission.Phase = task.PhaseFailed
	fixture.submission.OperationExecutionResult.Apply = nil
	fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "no durable Apply evidence returned"}

	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseFailed || completed.OperationExecutionResult == nil || completed.OperationExecutionResult.Apply != nil {
		t.Fatalf("completed=%#v", completed)
	}
	after, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status.State != operation.StateRunning || after.Status.Checkpoint != operationActionResultCheckpoint(task.OperationActionApply, task.PhaseFailed) {
		t.Fatalf("after status=%#v", after.Status)
	}
	if !reflect.DeepEqual(after.Execution, before.Execution) {
		t.Fatalf("failed Apply without evidence changed snapshot: before=%#v after=%#v", before.Execution, after.Execution)
	}
}

func TestMalformedOrMixedFailedApplyEvidenceIsRejectedBeforeWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*task.OperationExecutionResult)
	}{
		{"empty_checkpoint", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			apply.Checkpoint = ""
			result.Apply = &apply
		}},
		{"empty_schema", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			apply.State.SchemaVersion = ""
			result.Apply = &apply
		}},
		{"empty_payload", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			apply.State.Payload = nil
			result.Apply = &apply
		}},
		{"invalid_json", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			apply.State.Payload = json.RawMessage(`{"committed":`)
			result.Apply = &apply
		}},
		{"mixed_verification", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			result.Apply = &apply
			result.Verification = &operation.Verification{Passed: false}
		}},
		{"mixed_rollback", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			result.Apply = &apply
			result.Rollback = &operation.RollbackResult{Restored: true}
		}},
		{"mixed_restore_point", func(result *task.OperationExecutionResult) {
			apply := validFailedApplyEvidence()
			result.Apply = &apply
			result.RestorePoint = &operation.RestorePoint{ID: "unexpected"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, task.OperationActionApply)
			defer fixture.store.Close()
			beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
			fixture.submission.Phase = task.PhaseFailed
			fixture.submission.OperationExecutionResult.Apply = nil
			fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "failed"}
			test.mutate(fixture.submission.OperationExecutionResult)

			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
				t.Fatal("malformed or mixed failed Apply evidence must be rejected")
			}
			assertRejectedOperationExecutionResultLeftNoWrites(t, fixture, beforeRun, beforeJournal)
		})
	}
}

func TestOtherMutationAndCanceledApplyOutputsRemainRejected(t *testing.T) {
	tests := []struct {
		name   string
		action task.OperationAction
		phase  task.Phase
	}{
		{name: "failed_create_restore_point", action: task.OperationActionCreateRestorePoint, phase: task.PhaseFailed},
		{name: "failed_rollback", action: task.OperationActionRollback, phase: task.PhaseFailed},
		{name: "canceled_apply", action: task.OperationActionApply, phase: task.PhaseCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, test.action)
			defer fixture.store.Close()
			beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
			fixture.submission.Phase = test.phase
			fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "action_failed", Message: "failed"}

			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
				t.Fatal("forbidden mutation/canceled output must be rejected")
			}
			assertRejectedOperationExecutionResultLeftNoWrites(t, fixture, beforeRun, beforeJournal)
		})
	}
}

func TestFailedApplyEvidenceRollsBackWithTaskWhenCheckpointWriteFails(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionApply)
	defer fixture.store.Close()
	beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	beforeEvents := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_events WHERE task_id = ?`, fixture.taskID)
	apply := validFailedApplyEvidence()
	fixture.submission.Phase = task.PhaseFailed
	fixture.submission.OperationExecutionResult.Apply = &apply
	fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "partial mutation failed"}
	if _, err := fixture.store.db.Exec(`CREATE TRIGGER c31b_force_checkpoint_failure BEFORE UPDATE OF checkpoint ON operation_runs
		WHEN OLD.id = '` + fixture.runID + `' BEGIN SELECT RAISE(ABORT, 'forced c31b checkpoint failure'); END;`); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
		t.Fatal("forced checkpoint failure must reject failed Apply completion")
	}
	storedTask, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status.Phase != task.PhaseRunning || storedTask.OperationExecutionResult != nil {
		t.Fatalf("task terminalized despite rollback: %#v", storedTask)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 0 {
		t.Fatalf("task result survived rollback=%d", got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_events WHERE task_id = ?`, fixture.taskID); got != beforeEvents {
		t.Fatalf("task event survived rollback: before=%d after=%d", beforeEvents, got)
	}
	afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.Status.State != beforeRun.Status.State || afterRun.Status.Checkpoint != beforeRun.Status.Checkpoint || !reflect.DeepEqual(afterRun.Execution, beforeRun.Execution) {
		t.Fatalf("operation changed on rollback: before=%#v after=%#v", beforeRun, afterRun)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("journal survived rollback: before=%d after=%d", beforeJournal, got)
	}
}

func validFailedApplyEvidence() operation.ApplyResult {
	return operation.ApplyResult{
		Changed:    false,
		Checkpoint: "apply_partial",
		State: operation.Artifact{
			SchemaVersion: "clickhouse.apply.v1",
			Payload:       json.RawMessage(`{"run_id":"run-c31-apply","committed":[]}`),
		},
	}
}

func assertRejectedOperationExecutionResultLeftNoWrites(t *testing.T, fixture *operationExecutionFixture, beforeRun operationrun.Resource, beforeJournal int) {
	t.Helper()
	storedTask, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status.Phase != task.PhaseRunning || storedTask.OperationExecutionResult != nil {
		t.Fatalf("rejected result mutated task=%#v", storedTask)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 0 {
		t.Fatalf("rejected result persisted=%d", got)
	}
	afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.Status.State != beforeRun.Status.State || afterRun.Status.Checkpoint != beforeRun.Status.Checkpoint || !reflect.DeepEqual(afterRun.Execution, beforeRun.Execution) {
		t.Fatalf("rejected result mutated operation: before=%#v after=%#v", beforeRun, afterRun)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("rejected result appended journal: before=%d after=%d", beforeJournal, got)
	}
}
