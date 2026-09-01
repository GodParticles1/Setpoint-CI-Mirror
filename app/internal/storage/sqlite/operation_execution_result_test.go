package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestOperationExecutionResultRoundTripAndSnapshotMapping(t *testing.T) {
	for _, action := range []task.OperationAction{
		task.OperationActionCreateRestorePoint,
		task.OperationActionApply,
		task.OperationActionVerify,
		task.OperationActionRollback,
		task.OperationActionVerifyRollback,
	} {
		t.Run(string(action), func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, action)
			defer fixture.store.Close()

			beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			beforeJournal, err := fixture.store.List(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)

			completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt)
			if err != nil {
				t.Fatal(err)
			}
			if completed.Status.Phase != task.PhaseSucceeded || completed.OperationExecutionResult == nil {
				t.Fatalf("completed=%#v", completed)
			}
			if !reflect.DeepEqual(*completed.OperationExecutionResult, *fixture.submission.OperationExecutionResult) {
				t.Fatalf("round trip result=%#v want=%#v", completed.OperationExecutionResult, fixture.submission.OperationExecutionResult)
			}
			afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			if afterRun.Status.State != beforeRun.Status.State {
				t.Fatalf("C3.1 advanced lifecycle: before=%s after=%s", beforeRun.Status.State, afterRun.Status.State)
			}
			if afterRun.Status.Checkpoint != operationActionResultCheckpoint(action, task.PhaseSucceeded) {
				t.Fatalf("checkpoint=%s", afterRun.Status.Checkpoint)
			}
			assertOnlyActionSnapshotMerged(t, action, beforeRun.Execution, afterRun.Execution)

			afterJournal, err := fixture.store.List(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			if len(afterJournal) != len(beforeJournal)+1 {
				t.Fatalf("journal before=%d after=%d", len(beforeJournal), len(afterJournal))
			}
			tail := afterJournal[len(afterJournal)-1]
			if tail.Sequence != int64(len(afterJournal)) || tail.State != beforeRun.Status.State || tail.Checkpoint != afterRun.Status.Checkpoint {
				t.Fatalf("journal tail=%#v", tail)
			}
			if len(tail.Evidence) < 3 || tail.Evidence[0].ID != fixture.taskID || tail.Evidence[1].ID != string(action) || tail.Evidence[2].ID != string(task.PhaseSucceeded) {
				t.Fatalf("journal evidence=%#v", tail.Evidence)
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks {
				t.Fatalf("next action task created: before=%d after=%d", beforeTasks, got)
			}
		})
	}
}

func TestOperationExecutionResultRejectsCorrelationAndStaleResultsBeforeWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*operationExecutionFixture)
	}{
		{"operation_id", func(f *operationExecutionFixture) {
			f.submission.OperationExecutionResult.OperationID = "operation.other"
		}},
		{"run_id", func(f *operationExecutionFixture) { f.submission.OperationExecutionResult.RunID = "run-other" }},
		{"action", func(f *operationExecutionFixture) {
			f.submission.OperationExecutionResult.Action = task.OperationActionVerify
		}},
		{"wrong_task_id", func(f *operationExecutionFixture) {
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET task_id = ? WHERE id = ?`, f.planningTaskID, f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"stale_state", func(f *operationExecutionFixture) {
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET state = ? WHERE id = ?`, operation.StateVerifying, f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"operation_version", func(f *operationExecutionFixture) {
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET operation_version = 'drift' WHERE id = ?`, f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"capability_digest", func(f *operationExecutionFixture) {
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET capability_digest = 'sha256:drift' WHERE id = ?`, f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"node_id", func(f *operationExecutionFixture) {
			if _, err := f.store.RegisterNode(f.ctx, domain.Registration{AgentID: "node-other", Hostname: "other", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: f.base}); err != nil {
				f.t.Fatal(err)
			}
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET node_id = 'node-other' WHERE id = ?`, f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"plan_digest", func(f *operationExecutionFixture) {
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET plan_digest = 'sha256:drift' WHERE id = ?`, f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"targets", func(f *operationExecutionFixture) {
			targets, _ := json.Marshal([]operation.Target{{Kind: operation.TargetNode, NodeID: "node-other"}})
			if _, err := f.store.db.Exec(`UPDATE operation_runs SET targets_json = ? WHERE id = ?`, string(targets), f.runID); err != nil {
				f.t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
			defer fixture.store.Close()
			beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
			test.mutate(fixture)
			beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
				t.Fatal("mismatched or stale execution result must be rejected")
			}
			storedTask, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
			if err != nil {
				t.Fatal(err)
			}
			if storedTask.Status.Phase != task.PhaseRunning || storedTask.OperationExecutionResult != nil {
				t.Fatalf("rejected result mutated task=%#v", storedTask)
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 0 {
				t.Fatalf("rejected result persisted task_results=%d", got)
			}
			afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			if afterRun.Status.Checkpoint != beforeRun.Status.Checkpoint || !reflect.DeepEqual(afterRun.Execution, beforeRun.Execution) {
				t.Fatalf("rejected result mutated operation run: before=%#v after=%#v", beforeRun, afterRun)
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
				t.Fatalf("rejected result appended journal: before=%d after=%d", beforeJournal, got)
			}
		})
	}
}

func TestOperationExecutionSubmissionRequiresExactlyOneResultType(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	resource, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}

	withCheck := fixture.submission
	withCheck.Result = &task.CheckResult{}
	if _, _, err := encodeTaskSubmission(resource, withCheck); err == nil {
		t.Fatal("operation execution result mixed with check result must fail")
	}
	withPlanning := fixture.submission
	withPlanning.OperationResult = &operation.PlanningResult{}
	if _, _, err := encodeTaskSubmission(resource, withPlanning); err == nil {
		t.Fatal("operation execution result mixed with planning result must fail")
	}
}

func TestOperationExecutionResultRejectsMixedAndOptimisticFailureShapes(t *testing.T) {
	tests := []struct {
		name       string
		phase      task.Phase
		mutate     func(*task.OperationExecutionResult)
		wantErrSet bool
	}{
		{"mixed_success", task.PhaseSucceeded, func(result *task.OperationExecutionResult) {
			result.Apply = &operation.ApplyResult{Changed: true}
		}, false},
		{"success_with_error", task.PhaseSucceeded, func(result *task.OperationExecutionResult) {
			result.Error = &task.Failure{Code: "unexpected", Message: "unexpected"}
		}, false},
		{"failed_with_output", task.PhaseFailed, func(result *task.OperationExecutionResult) {
			result.Error = &task.Failure{Code: "create_restore_point_failed", Message: "failed"}
		}, true},
		{"failed_without_error", task.PhaseFailed, func(result *task.OperationExecutionResult) {
			result.RestorePoint = nil
		}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
			defer fixture.store.Close()
			fixture.submission.Phase = test.phase
			test.mutate(fixture.submission.OperationExecutionResult)
			if test.name == "failed_with_output" && fixture.submission.OperationExecutionResult.RestorePoint == nil {
				t.Fatal("fixture must retain optimistic output")
			}
			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
				t.Fatal("invalid action result shape must fail")
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 0 {
				t.Fatalf("invalid result persisted=%d", got)
			}
		})
	}
}

func TestOperationExecutionNegativeVerificationEvidenceIsDurableAndIdempotent(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionVerify, task.OperationActionVerifyRollback} {
		t.Run(string(action), func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, action)
			defer fixture.store.Close()
			beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
			beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)
			fixture.submission.Phase = task.PhaseFailed
			fixture.submission.OperationExecutionResult.Verification = &operation.Verification{Passed: false, Summary: "verification did not pass"}
			fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "verification_failed", Message: "verification did not pass"}

			completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt)
			if err != nil {
				t.Fatal(err)
			}
			if completed.Status.Phase != task.PhaseFailed || completed.OperationExecutionResult == nil || completed.OperationExecutionResult.Verification == nil || completed.OperationExecutionResult.Verification.Passed {
				t.Fatalf("completed=%#v", completed)
			}
			afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			if afterRun.Status.State != beforeRun.Status.State || afterRun.Status.Checkpoint != operationActionResultCheckpoint(action, task.PhaseFailed) {
				t.Fatalf("after status=%#v", afterRun.Status)
			}
			assertOnlyActionSnapshotMerged(t, action, beforeRun.Execution, afterRun.Execution)
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 1 {
				t.Fatalf("task result rows=%d", got)
			}
			journalCount := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
			if journalCount != beforeJournal+1 {
				t.Fatalf("journal before=%d after=%d", beforeJournal, journalCount)
			}
			journal, err := fixture.store.List(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			tail := journal[len(journal)-1]
			if tail.State != beforeRun.Status.State || tail.Checkpoint != afterRun.Status.Checkpoint || len(tail.Evidence) != 4 || tail.Evidence[2].ID != string(task.PhaseFailed) || tail.Evidence[3].ID != "verification_failed" {
				t.Fatalf("journal tail=%#v", tail)
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks {
				t.Fatalf("next action task created: before=%d after=%d", beforeTasks, got)
			}

			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt.Add(time.Minute)); err != nil {
				t.Fatalf("identical negative verification retry: %v", err)
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
				t.Fatalf("identical retry appended journal: before=%d after=%d", journalCount, got)
			}

			conflicting := fixture.submission
			resultCopy := *fixture.submission.OperationExecutionResult
			verificationCopy := *resultCopy.Verification
			verificationCopy.Summary = "different negative verification fact"
			resultCopy.Verification = &verificationCopy
			conflicting.OperationExecutionResult = &resultCopy
			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, conflicting, fixture.reportedAt.Add(2*time.Minute)); !errors.Is(err, task.ErrResultConflict) {
				t.Fatalf("conflicting retry error=%v", err)
			}
		})
	}
}

func TestOperationExecutionVerificationResultShapeRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name   string
		action task.OperationAction
		phase  task.Phase
		passed bool
	}{
		{"failed_verify_positive", task.OperationActionVerify, task.PhaseFailed, true},
		{"failed_verify_rollback_positive", task.OperationActionVerifyRollback, task.PhaseFailed, true},
		{"succeeded_verify_negative", task.OperationActionVerify, task.PhaseSucceeded, false},
		{"succeeded_verify_rollback_negative", task.OperationActionVerifyRollback, task.PhaseSucceeded, false},
		{"canceled_verify_negative", task.OperationActionVerify, task.PhaseCanceled, false},
		{"canceled_verify_rollback_negative", task.OperationActionVerifyRollback, task.PhaseCanceled, false},
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
			fixture.submission.OperationExecutionResult.Verification = &operation.Verification{Passed: test.passed, Summary: "shape test"}
			if test.phase != task.PhaseSucceeded {
				fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "verification_failed", Message: "verification failed"}
			}

			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
				t.Fatal("invalid verification evidence must be rejected")
			}
			assertOperationExecutionResultRejectedWithoutDurableWrite(t, fixture, beforeRun, beforeJournal)
		})
	}
}

func TestOperationExecutionFailedMutationOutputsAreRejected(t *testing.T) {
	for _, action := range []task.OperationAction{
		task.OperationActionCreateRestorePoint,
		task.OperationActionRollback,
	} {
		t.Run(string(action), func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, action)
			defer fixture.store.Close()
			beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
			fixture.submission.Phase = task.PhaseFailed
			fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "mutation_failed", Message: "mutation failed"}

			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
				t.Fatal("failed mutation output must be rejected")
			}
			assertOperationExecutionResultRejectedWithoutDurableWrite(t, fixture, beforeRun, beforeJournal)
		})
	}
}

func TestOperationExecutionNegativeVerificationRollsBackAtomicallyOnCheckpointFailure(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionVerify)
	defer fixture.store.Close()
	beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	beforeEvents := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_events WHERE task_id = ?`, fixture.taskID)
	fixture.submission.Phase = task.PhaseFailed
	fixture.submission.OperationExecutionResult.Verification = &operation.Verification{Passed: false, Summary: "verification did not pass"}
	fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "verification_failed", Message: "verification did not pass"}
	if _, err := fixture.store.db.Exec(`CREATE TRIGGER c31_negative_verify_checkpoint_failure BEFORE UPDATE OF checkpoint ON operation_runs
		WHEN OLD.id = '` + fixture.runID + `' BEGIN SELECT RAISE(ABORT, 'forced negative verification checkpoint failure'); END;`); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
		t.Fatal("forced checkpoint failure must fail negative verification completion")
	}
	storedTask, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status.Phase != task.PhaseRunning || storedTask.OperationExecutionResult != nil {
		t.Fatalf("task terminalized despite transaction rollback: %#v", storedTask)
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
		t.Fatalf("operation run changed on rollback: before=%#v after=%#v", beforeRun, afterRun)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("journal survived rollback: before=%d after=%d", beforeJournal, got)
	}
}

func assertOperationExecutionResultRejectedWithoutDurableWrite(t *testing.T, fixture *operationExecutionFixture, beforeRun operationrun.Resource, beforeJournal int) {
	t.Helper()
	storedTask, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status.Phase != task.PhaseRunning || storedTask.OperationExecutionResult != nil {
		t.Fatalf("rejected result mutated task=%#v", storedTask)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 0 {
		t.Fatalf("rejected result persisted task_results=%d", got)
	}
	afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.Status.State != beforeRun.Status.State || afterRun.Status.Checkpoint != beforeRun.Status.Checkpoint || !reflect.DeepEqual(afterRun.Execution, beforeRun.Execution) {
		t.Fatalf("rejected result mutated operation run: before=%#v after=%#v", beforeRun, afterRun)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("rejected result appended journal: before=%d after=%d", beforeJournal, got)
	}
}

func TestOperationExecutionFailedResultPersistsNoOptimisticSnapshot(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionApply)
	defer fixture.store.Close()
	before, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.submission.Phase = task.PhaseFailed
	fixture.submission.OperationExecutionResult.Apply = nil
	fixture.submission.OperationExecutionResult.Error = &task.Failure{Code: "apply_failed", Message: "bounded apply failed"}

	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseFailed || completed.Status.LastError == nil || completed.Status.LastError.Code != "apply_failed" {
		t.Fatalf("completed=%#v", completed)
	}
	after, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Execution, before.Execution) || after.Execution == nil || after.Execution.Apply != nil {
		t.Fatalf("failed action persisted optimistic output: before=%#v after=%#v", before.Execution, after.Execution)
	}
	if after.Status.State != before.Status.State || after.Status.Checkpoint != operationActionResultCheckpoint(task.OperationActionApply, task.PhaseFailed) {
		t.Fatalf("after status=%#v", after.Status)
	}
}

func TestOperationExecutionCompletionRollsBackTaskWhenCheckpointWriteFails(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionApply)
	defer fixture.store.Close()
	beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	beforeEvents := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_events WHERE task_id = ?`, fixture.taskID)
	if _, err := fixture.store.db.Exec(`CREATE TRIGGER c31_force_checkpoint_failure BEFORE UPDATE OF checkpoint ON operation_runs
		WHEN OLD.id = '` + fixture.runID + `' BEGIN SELECT RAISE(ABORT, 'forced c31 checkpoint failure'); END;`); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err == nil {
		t.Fatal("forced checkpoint failure must fail task completion")
	}
	storedTask, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status.Phase != task.PhaseRunning || storedTask.OperationExecutionResult != nil {
		t.Fatalf("task terminalized despite transaction rollback: %#v", storedTask)
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
		t.Fatalf("operation run changed on rollback: before=%#v after=%#v", beforeRun, afterRun)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("journal survived rollback: before=%d after=%d", beforeJournal, got)
	}
}

func TestOperationExecutionTerminalRetryIsIdempotentAndConflictRejected(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatal(err)
	}
	journalCount := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt.Add(time.Minute)); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
		t.Fatalf("identical retry appended journal: before=%d after=%d", journalCount, got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 1 {
		t.Fatalf("identical retry duplicated task result=%d", got)
	}

	conflicting := fixture.submission
	resultCopy := *fixture.submission.OperationExecutionResult
	pointCopy := *resultCopy.RestorePoint
	pointCopy.ID = "restore-conflict"
	resultCopy.RestorePoint = &pointCopy
	conflicting.OperationExecutionResult = &resultCopy
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, conflicting, fixture.reportedAt.Add(2*time.Minute)); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("conflicting retry error=%v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
		t.Fatalf("conflicting retry appended journal: before=%d after=%d", journalCount, got)
	}
}

type operationExecutionFixture struct {
	t              *testing.T
	ctx            context.Context
	store          *Store
	base           time.Time
	runID          string
	planningTaskID string
	taskID         string
	claimID        string
	nodeID         string
	reportedAt     time.Time
	submission     task.ResultSubmission
}

func prepareOperationExecutionFixture(t *testing.T, action task.OperationAction) *operationExecutionFixture {
	return prepareOperationExecutionFixtureWithSecretRefs(t, action, nil)
}

func prepareOperationExecutionFixtureWithSecretRefs(t *testing.T, action task.OperationAction, secretRefs []operation.SecretRef) *operationExecutionFixture {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-c31-" + string(action)
	planningTaskID := "plan-c31-" + string(action)
	prepareAwaitingOperationRunWithSecretRefs(t, store, base, runID, planningTaskID, secretRefs)
	point := testOperationRestorePoint(runID, base)
	apply := operation.ApplyResult{Changed: true, Checkpoint: "apply_complete", State: operation.Artifact{SchemaVersion: "test.apply.v1", Payload: json.RawMessage(`{"changed":true}`)}}
	rollback := operation.RollbackResult{Restored: true, Checkpoint: "rollback_complete", State: operation.Artifact{SchemaVersion: "test.rollback.v1", Payload: json.RawMessage(`{"restored":true}`)}}

	sequence := int64(0)
	checkpointAt := base.Add(10 * time.Second)
	advance := func(state operation.State, checkpoint string, snapshot operationrun.ExecutionSnapshot) {
		t.Helper()
		sequence++
		checkpointAt = checkpointAt.Add(time.Second)
		entry := operation.JournalEntry{RunID: runID, Sequence: sequence, State: state, Checkpoint: checkpoint, Message: "C3.1 fixture checkpoint", At: checkpointAt}
		if _, err := store.SaveOperationExecutionCheckpoint(ctx, runID, state, checkpoint, snapshot, nil, entry, checkpointAt); err != nil {
			t.Fatalf("seed state=%s checkpoint=%s: %v", state, checkpoint, err)
		}
	}
	advance(operation.StateQueued, "confirmed", operationrun.ExecutionSnapshot{})
	advance(operation.StateAcquiringLock, "acquire_lock", operationrun.ExecutionSnapshot{})
	advance(operation.StateCreatingRestorePoint, "create_restore_point", operationrun.ExecutionSnapshot{})
	if action != task.OperationActionCreateRestorePoint {
		advance(operation.StateCreatingRestorePoint, "action_create_restore_point_succeeded", operationrun.ExecutionSnapshot{RestorePoint: &point})
		advance(operation.StateRunning, "apply", operationrun.ExecutionSnapshot{})
	}
	if action == task.OperationActionVerify || action == task.OperationActionRollback || action == task.OperationActionVerifyRollback {
		advance(operation.StateRunning, "action_apply_succeeded", operationrun.ExecutionSnapshot{Apply: &apply})
	}
	if action == task.OperationActionVerify {
		advance(operation.StateVerifying, "verify", operationrun.ExecutionSnapshot{})
	}
	if action == task.OperationActionRollback || action == task.OperationActionVerifyRollback {
		advance(operation.StateRollingBack, "rollback", operationrun.ExecutionSnapshot{})
	}
	if action == task.OperationActionVerifyRollback {
		advance(operation.StateRollingBack, "action_rollback_succeeded", operationrun.ExecutionSnapshot{Rollback: &rollback})
	}

	run, err := store.GetOperationRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	contractInput := task.OperationExecutionContract{
		OperationID: run.Spec.OperationID,
		RunID:       runID,
		Action:      action,
		PlanDigest:  run.PlanDigest,
		Targets:     run.Spec.Targets,
		Plan:        *run.Plan,
	}
	switch action {
	case task.OperationActionApply:
		contractInput.Impact = run.Impact
		contractInput.RestorePoint = run.Execution.RestorePoint
	case task.OperationActionVerify:
		contractInput.Apply = run.Execution.Apply
	case task.OperationActionRollback:
		contractInput.RestorePoint = run.Execution.RestorePoint
		contractInput.Apply = run.Execution.Apply
	case task.OperationActionVerifyRollback:
		contractInput.RestorePoint = run.Execution.RestorePoint
		contractInput.Rollback = run.Execution.Rollback
	}
	contract, digest, err := task.NewOperationExecutionContract(contractInput)
	if err != nil {
		t.Fatal(err)
	}
	taskID := "action-c31-" + string(action)
	actionTask := task.Resource{
		APIVersion: "setpoint.io/v1",
		Kind:       task.KindOperationExecutionTask,
		Metadata:   task.Metadata{ID: taskID, IdempotencyKey: runID + ":" + string(action), CreatedAt: checkpointAt.Add(time.Second)},
		Spec: task.Spec{
			NodeID:             run.Spec.NodeID,
			OperationID:        run.Spec.OperationID,
			OperationVersion:   run.Spec.OperationVersion,
			CapabilityDigest:   run.Spec.CapabilityDigest,
			Targets:            append([]operation.Target(nil), run.Spec.Targets...),
			Parameters:         append(json.RawMessage(nil), run.Spec.Parameters...),
			SecretRefs:         append([]operation.SecretRef(nil), run.Spec.SecretRefs...),
			OperationExecution: &contract,
			ContractDigest:     digest,
		},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: checkpointAt.Add(time.Second)},
	}
	if _, created, err := store.CreateTask(ctx, actionTask); err != nil || !created {
		t.Fatalf("create action task created=%v err=%v", created, err)
	}
	claimID := "claim-c31-" + string(action)
	claimed, err := store.ClaimTask(ctx, run.Spec.NodeID, claimID, checkpointAt.Add(2*time.Second))
	if err != nil || claimed == nil || claimed.Metadata.ID != taskID {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := store.AcknowledgeTask(ctx, run.Spec.NodeID, taskID, claimID, checkpointAt.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE operation_runs SET task_id = ? WHERE id = ?`, taskID, runID); err != nil {
		t.Fatal(err)
	}

	result := task.OperationExecutionResult{OperationID: run.Spec.OperationID, RunID: runID, Action: action}
	switch action {
	case task.OperationActionCreateRestorePoint:
		result.RestorePoint = &point
	case task.OperationActionApply:
		result.Apply = &apply
	case task.OperationActionVerify:
		result.Verification = &operation.Verification{Passed: true, Summary: "verified"}
	case task.OperationActionRollback:
		result.Rollback = &rollback
	case task.OperationActionVerifyRollback:
		result.Verification = &operation.Verification{Passed: true, Summary: "rollback verified"}
	}
	return &operationExecutionFixture{
		t: t, ctx: ctx, store: store, base: base, runID: runID, planningTaskID: planningTaskID,
		taskID: taskID, claimID: claimID, nodeID: run.Spec.NodeID, reportedAt: checkpointAt.Add(4 * time.Second),
		submission: task.ResultSubmission{ClaimID: claimID, Phase: task.PhaseSucceeded, OperationExecutionResult: &result},
	}
}

func testOperationRestorePoint(runID string, base time.Time) operation.RestorePoint {
	return operation.RestorePoint{
		ID: "restore-" + runID, ProviderID: "test.restore", OperationID: "operation.test", RunID: runID,
		Status:    operation.RestorePointVerified,
		Targets:   []operation.Target{{Kind: operation.TargetNode, NodeID: "node-execution"}},
		CreatedAt: base,
		Manifest:  operation.Artifact{SchemaVersion: "test.restore.v1", Payload: json.RawMessage(`{"owned":true}`)},
	}
}

func assertOnlyActionSnapshotMerged(t *testing.T, action task.OperationAction, before, after *operationrun.ExecutionSnapshot) {
	t.Helper()
	if after == nil {
		t.Fatal("execution snapshot is nil")
	}
	beforeValue := operationrun.ExecutionSnapshot{}
	if before != nil {
		beforeValue = *before
	}
	switch action {
	case task.OperationActionCreateRestorePoint:
		if after.RestorePoint == nil || beforeValue.RestorePoint != nil || !reflect.DeepEqual(after.Apply, beforeValue.Apply) || !reflect.DeepEqual(after.Verification, beforeValue.Verification) || !reflect.DeepEqual(after.Rollback, beforeValue.Rollback) || !reflect.DeepEqual(after.RollbackVerification, beforeValue.RollbackVerification) {
			t.Fatalf("unexpected restore-point merge before=%#v after=%#v", before, after)
		}
	case task.OperationActionApply:
		if after.Apply == nil || beforeValue.Apply != nil || !reflect.DeepEqual(after.RestorePoint, beforeValue.RestorePoint) || !reflect.DeepEqual(after.Verification, beforeValue.Verification) || !reflect.DeepEqual(after.Rollback, beforeValue.Rollback) || !reflect.DeepEqual(after.RollbackVerification, beforeValue.RollbackVerification) {
			t.Fatalf("unexpected apply merge before=%#v after=%#v", before, after)
		}
	case task.OperationActionVerify:
		if after.Verification == nil || beforeValue.Verification != nil || !reflect.DeepEqual(after.RestorePoint, beforeValue.RestorePoint) || !reflect.DeepEqual(after.Apply, beforeValue.Apply) || !reflect.DeepEqual(after.Rollback, beforeValue.Rollback) || !reflect.DeepEqual(after.RollbackVerification, beforeValue.RollbackVerification) {
			t.Fatalf("unexpected verify merge before=%#v after=%#v", before, after)
		}
	case task.OperationActionRollback:
		if after.Rollback == nil || beforeValue.Rollback != nil || !reflect.DeepEqual(after.RestorePoint, beforeValue.RestorePoint) || !reflect.DeepEqual(after.Apply, beforeValue.Apply) || !reflect.DeepEqual(after.Verification, beforeValue.Verification) || !reflect.DeepEqual(after.RollbackVerification, beforeValue.RollbackVerification) {
			t.Fatalf("unexpected rollback merge before=%#v after=%#v", before, after)
		}
	case task.OperationActionVerifyRollback:
		if after.RollbackVerification == nil || beforeValue.RollbackVerification != nil || !reflect.DeepEqual(after.RestorePoint, beforeValue.RestorePoint) || !reflect.DeepEqual(after.Apply, beforeValue.Apply) || !reflect.DeepEqual(after.Verification, beforeValue.Verification) || !reflect.DeepEqual(after.Rollback, beforeValue.Rollback) {
			t.Fatalf("unexpected rollback verification merge before=%#v after=%#v", before, after)
		}
	}
}

func countRows(t *testing.T, store *Store, query string, args ...any) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
