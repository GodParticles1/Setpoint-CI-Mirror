package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func TestContinueOperationRunCommitsAndRetriesIdempotently(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatal(err)
	}

	next, journal, at := continuationApplyInput(t, fixture)
	beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)

	continued, err := fixture.store.ContinueOperationRun(fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", next, journal, at)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Status.State != operation.StateRunning || continued.Status.Checkpoint != "apply_queued" || continued.Status.TaskID != next.Metadata.ID {
		t.Fatalf("continued=%#v", continued.Status)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks+1 {
		t.Fatalf("task count=%d want=%d", got, beforeTasks+1)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal+1 {
		t.Fatalf("journal count=%d want=%d", got, beforeJournal+1)
	}

	if _, err := fixture.store.ContinueOperationRun(fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", next, journal, at); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks+1 {
		t.Fatalf("identical retry duplicated task: %d", got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal+1 {
		t.Fatalf("identical retry duplicated journal: %d", got)
	}

	conflicting := next
	contractCopy := *next.Spec.OperationExecution
	contractCopy.Action = task.OperationActionVerify
	conflicting.Spec.OperationExecution = &contractCopy
	conflicting.Spec.ContractDigest = "conflicting-contract"
	if _, err := fixture.store.ContinueOperationRun(fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", conflicting, journal, at); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("conflicting next action error=%v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks+1 {
		t.Fatalf("conflicting retry mutated tasks: %d", got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal+1 {
		t.Fatalf("conflicting retry mutated journal: %d", got)
	}
}

func TestContinueOperationRunRejectsConflictingCompletedTask(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatal(err)
	}
	next, journal, at := continuationApplyInput(t, fixture)
	beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)

	if _, err := fixture.store.ContinueOperationRun(fixture.ctx, fixture.runID, "different-completed-task", operation.StateRunning, "apply_queued", next, journal, at); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("error=%v", err)
	}
	afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRun.Status, afterRun.Status) {
		t.Fatalf("run changed: before=%#v after=%#v", beforeRun.Status, afterRun.Status)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks {
		t.Fatalf("tasks changed=%d", got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("journal changed=%d", got)
	}
}

func TestContinueOperationRunRejectsTargetsOutsideDurablePlan(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatal(err)
	}
	next, journal, at := continuationApplyInput(t, fixture)
	foreign := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.foreign"}
	next.Spec.Targets = append(next.Spec.Targets, foreign)
	next.Spec.OperationExecution.Targets = append(next.Spec.OperationExecution.Targets, foreign)
	if _, err := fixture.store.ContinueOperationRun(fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", next, journal, at); err == nil {
		t.Fatal("operation continuation accepted targets outside the durable plan")
	}
}

func TestContinueOperationRunRollsBackTransactionOnRunUpdateFailure(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatal(err)
	}
	next, journal, at := continuationApplyInput(t, fixture)
	beforeRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	if _, err := fixture.store.db.Exec(`
		CREATE TRIGGER fail_operation_continuation
		BEFORE UPDATE OF task_id ON operation_runs
		BEGIN
			SELECT RAISE(ABORT, 'forced continuation failure');
		END`); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.ContinueOperationRun(fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", next, journal, at); err == nil {
		t.Fatal("expected forced continuation failure")
	}
	afterRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRun.Status, afterRun.Status) {
		t.Fatalf("run survived failed transaction: before=%#v after=%#v", beforeRun.Status, afterRun.Status)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks {
		t.Fatalf("next task survived rollback: before=%d after=%d", beforeTasks, got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal {
		t.Fatalf("journal survived rollback: before=%d after=%d", beforeJournal, got)
	}
}

func continuationApplyInput(t *testing.T, fixture *operationExecutionFixture) (task.Resource, operation.JournalEntry, time.Time) {
	t.Helper()
	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Plan == nil || run.Impact == nil || run.Execution == nil || run.Execution.RestorePoint == nil {
		t.Fatalf("run lacks Apply prerequisites: %#v", run)
	}
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID:  run.Spec.OperationID,
		RunID:        run.Metadata.ID,
		Action:       task.OperationActionApply,
		PlanDigest:   run.PlanDigest,
		Targets:      append([]operation.Target(nil), run.Spec.Targets...),
		Plan:         *run.Plan,
		Impact:       run.Impact,
		RestorePoint: run.Execution.RestorePoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	at := fixture.reportedAt.Add(time.Second)
	taskID := run.Metadata.ID + ":apply"
	next := task.Resource{
		APIVersion: "setpoint.io/v1",
		Kind:       task.KindOperationExecutionTask,
		Metadata:   task.Metadata{ID: taskID, IdempotencyKey: taskID, CreatedAt: at},
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
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: at},
	}
	entries, err := fixture.store.List(context.Background(), run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	journal := operation.JournalEntry{
		RunID: run.Metadata.ID, Sequence: entries[len(entries)-1].Sequence + 1,
		State: operation.StateRunning, Checkpoint: "apply_queued",
		Message: "Server queued bounded action apply", At: at,
		Evidence: []operation.EvidenceRef{{ID: taskID, Kind: "operation_action_task"}, {ID: string(task.OperationActionApply), Kind: "operation_action"}},
	}
	return next, journal, at
}
