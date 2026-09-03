package sqlite

import (
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func TestCanceledCreateRestorePointAcknowledgementConvergesTaskAndRun(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	cancelAt := fixture.reportedAt.Add(time.Second)
	canceled, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status.State != operation.StateCanceledBeforeApply {
		t.Fatalf("run state=%s", canceled.Status.State)
	}
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	submission := canceledExecutionSubmission(fixture)
	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, submission, cancelAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseCanceled {
		t.Fatalf("task phase=%s", completed.Status.Phase)
	}
	assertCanceledRunConverged(t, fixture, beforeJournal)
	claimed, err := fixture.store.ClaimTask(fixture.ctx, fixture.nodeID, "claim-after-cancel", cancelAt.Add(2*time.Second))
	if err != nil || claimed != nil {
		t.Fatalf("claim after convergence=%#v err=%v", claimed, err)
	}
}

func TestCanceledCreateRestorePointRetainsLateSuccessWithoutApply(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	cancelAt := fixture.reportedAt.Add(time.Second)
	if _, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt); err != nil {
		t.Fatal(err)
	}
	beforeJournal := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, cancelAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseSucceeded {
		t.Fatalf("late success phase=%s", completed.Status.Phase)
	}
	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status.State != operation.StateCanceledBeforeApply || run.Execution == nil || run.Execution.RestorePoint == nil {
		t.Fatalf("late physical fact was lost: run=%#v", run)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal+1 {
		t.Fatalf("journal count=%d want=%d", got, beforeJournal+1)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks WHERE operation_id = ? AND id <> ?`, run.Spec.OperationID, fixture.taskID); got != 1 {
		// The one pre-existing row is the completed planning task; no Apply task may exist.
		t.Fatalf("unexpected operation task count=%d", got)
	}
}

func TestCanceledExecutionConvergenceSurvivesRestartAndIsIdempotent(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	ctx := fixture.ctx
	store := fixture.store
	var sequence int
	var databaseName, path string
	if err := store.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &databaseName, &path); err != nil {
		t.Fatal(err)
	}
	cancelAt := fixture.reportedAt.Add(time.Second)
	if _, err := store.CancelOperationRun(ctx, fixture.runID, cancelAt); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := open(ctx, path, func() time.Time { return cancelAt.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture.store = store
	submission := canceledExecutionSubmission(fixture)
	if _, err := store.CompleteTask(ctx, fixture.nodeID, fixture.taskID, submission, cancelAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	journalCount := countRows(t, store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	if _, err := store.CompleteTask(ctx, fixture.nodeID, fixture.taskID, submission, cancelAt.Add(3*time.Second)); err != nil {
		t.Fatalf("duplicate cancellation acknowledgement: %v", err)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
		t.Fatalf("duplicate acknowledgement appended journal: before=%d after=%d", journalCount, got)
	}
	claimed, err := store.ClaimTask(ctx, fixture.nodeID, "claim-after-restart", cancelAt.Add(4*time.Second))
	if err != nil || claimed != nil {
		t.Fatalf("claim after restart convergence=%#v err=%v", claimed, err)
	}
}

func TestCancelBeforeClaimTerminalizesImmediatelyAndIsIdempotent(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.db.ExecContext(fixture.ctx, `UPDATE tasks SET phase=?, claim_id=NULL, attempt=0, claimed_at=NULL, acknowledged_at=NULL WHERE id=?`, task.PhasePending, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	cancelAt := fixture.reportedAt.Add(time.Second)
	if _, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != task.PhaseCanceled || stored.Status.CompletedAt == nil {
		t.Fatalf("pending task did not terminalize: %#v", stored.Status)
	}
	journalCount := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id=?`, fixture.runID)
	if _, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt.Add(time.Second)); err != nil {
		t.Fatalf("duplicate cancel: %v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id=?`, fixture.runID); got != journalCount {
		t.Fatalf("duplicate cancel appended journal: before=%d after=%d", journalCount, got)
	}
	claimed, err := fixture.store.ClaimTask(fixture.ctx, fixture.nodeID, "claim-after-pending-cancel", cancelAt.Add(2*time.Second))
	if err != nil || claimed != nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
}

func TestCancelAfterClaimConvergesThroughAgentAcknowledgement(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.db.ExecContext(fixture.ctx, `UPDATE tasks SET phase=?, acknowledged_at=NULL WHERE id=?`, task.PhaseClaimed, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	cancelAt := fixture.reportedAt.Add(time.Second)
	if _, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt); err != nil {
		t.Fatal(err)
	}
	acknowledged, err := fixture.store.AcknowledgeTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.claimID, cancelAt.Add(time.Second))
	if err != nil || acknowledged.Status.Phase != task.PhaseCancelRequested {
		t.Fatalf("acknowledged=%#v err=%v", acknowledged, err)
	}
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, canceledExecutionSubmission(fixture), cancelAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil || stored.Status.Phase != task.PhaseCanceled {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestAcceptedRestorePointThenCancelRetainsEvidenceAndNeverQueuesApply(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatal(err)
	}
	cancelAt := fixture.reportedAt.Add(time.Second)
	if _, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status.State != operation.StateCanceledBeforeApply || run.Execution == nil || run.Execution.RestorePoint == nil {
		t.Fatalf("cancellation lost accepted RestorePoint: %#v", run)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks WHERE id=?`, fixture.runID+":apply"); got != 0 {
		t.Fatalf("accepted RestorePoint cancellation queued Apply=%d", got)
	}
}

func TestCanceledCreateRestorePointFailureConvergesWithoutApply(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
	cancelAt := fixture.reportedAt.Add(time.Second)
	if _, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, cancelAt); err != nil {
		t.Fatal(err)
	}
	result := task.OperationExecutionResult{
		OperationID: "operation.test", RunID: fixture.runID, Action: task.OperationActionCreateRestorePoint,
		Error: &task.Failure{Code: "restore_point_failed", Message: "physical restore point creation failed"},
	}
	submission := task.ResultSubmission{ClaimID: fixture.claimID, Phase: task.PhaseFailed, OperationExecutionResult: &result}
	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, submission, cancelAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseFailed {
		t.Fatalf("task phase=%s", completed.Status.Phase)
	}
	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status.State != operation.StateCanceledBeforeApply || run.Execution != nil {
		t.Fatalf("failed pre-Apply task changed canceled truth: %#v", run)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks WHERE id=?`, fixture.runID+":apply"); got != 0 {
		t.Fatalf("failed canceled RestorePoint queued Apply=%d", got)
	}
}

func canceledExecutionSubmission(fixture *operationExecutionFixture) task.ResultSubmission {
	result := task.OperationExecutionResult{
		OperationID: "operation.test", RunID: fixture.runID, Action: task.OperationActionCreateRestorePoint,
		Error: &task.Failure{Code: "task_canceled", Message: "bounded action cancellation acknowledged"},
	}
	return task.ResultSubmission{ClaimID: fixture.claimID, Phase: task.PhaseCanceled, OperationExecutionResult: &result}
}

func assertCanceledRunConverged(t *testing.T, fixture *operationExecutionFixture, beforeJournal int) {
	t.Helper()
	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status.State != operation.StateCanceledBeforeApply {
		t.Fatalf("run state=%s", run.Status.State)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != beforeJournal+1 {
		t.Fatalf("journal count=%d want=%d", got, beforeJournal+1)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks WHERE id = ?`, fixture.runID+":apply"); got != 0 {
		t.Fatalf("canceled restore point queued Apply tasks=%d", got)
	}
}
