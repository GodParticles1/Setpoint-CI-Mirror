package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func TestCancelOperationRunAppendsExecutionJournalAtomically(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-cancel-journal", "task-cancel-journal")

	queuedAt := base.Add(time.Second)
	if err := store.Append(ctx, operation.JournalEntry{RunID: "run-cancel-journal", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "queued", At: queuedAt}); err != nil {
		t.Fatal(err)
	}
	canceledAt := base.Add(2 * time.Second)
	canceled, err := store.CancelOperationRun(ctx, "run-cancel-journal", canceledAt)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status.State != operation.StateCanceledBeforeApply || canceled.Status.Checkpoint != "canceled_before_apply" {
		t.Fatalf("canceled run=%#v", canceled.Status)
	}
	journal, err := store.List(ctx, "run-cancel-journal")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != 2 || journal[1].Sequence != 2 || journal[1].State != operation.StateCanceledBeforeApply || journal[1].Checkpoint != "canceled_before_apply" {
		t.Fatalf("journal=%#v", journal)
	}
	runtime, err := store.LoadOperationRuntimeSnapshot(ctx, "run-cancel-journal", canceledAt)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.JournalTail == nil || runtime.JournalTail.Sequence != 2 || runtime.Run.Status.State != operation.StateCanceledBeforeApply || runtime.WriteBlockedUntilReconcile {
		t.Fatalf("runtime=%#v", runtime)
	}
}

func TestCancelOperationRunFailsClosedWhenExecutionJournalIsMissing(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-cancel-corrupt", "task-cancel-corrupt")
	if _, err := store.db.ExecContext(ctx, `UPDATE operation_runs SET state='queued', checkpoint='confirmed' WHERE id='run-cancel-corrupt'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelOperationRun(ctx, "run-cancel-corrupt", base.Add(time.Second)); err == nil {
		t.Fatal("execution cancellation without durable journal must fail closed")
	}
	stored, err := store.GetOperationRun(ctx, "run-cancel-corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.State != operation.StateQueued || stored.Status.Checkpoint != "confirmed" {
		t.Fatalf("failed cancellation mutated run=%#v", stored.Status)
	}
}
