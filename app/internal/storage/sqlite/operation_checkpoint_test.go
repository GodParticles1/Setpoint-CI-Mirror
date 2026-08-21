package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
)

func TestOperationExecutionCheckpointUpdatesRunAndJournalAtomically(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-checkpoint", "task-checkpoint")

	queuedAt := base.Add(time.Second)
	queued := operation.JournalEntry{RunID: "run-checkpoint", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "operation queued", At: queuedAt}
	stored, err := store.SaveOperationExecutionCheckpoint(ctx, "run-checkpoint", operation.StateQueued, "confirmed", operationrun.ExecutionSnapshot{}, nil, queued, queuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.State != operation.StateQueued || stored.Status.Checkpoint != "confirmed" {
		t.Fatalf("stored=%#v", stored.Status)
	}
	if _, err := store.SaveOperationExecutionCheckpoint(ctx, "run-checkpoint", operation.StateQueued, "confirmed", operationrun.ExecutionSnapshot{}, nil, queued, queuedAt); err != nil {
		t.Fatalf("idempotent checkpoint retry: %v", err)
	}
	journal, err := store.List(ctx, "run-checkpoint")
	if err != nil || len(journal) != 1 {
		t.Fatalf("journal=%#v err=%v", journal, err)
	}

	lockAt := base.Add(2 * time.Second)
	lockEntry := operation.JournalEntry{RunID: "run-checkpoint", Sequence: 2, State: operation.StateAcquiringLock, Checkpoint: "acquire_lock", Message: "acquire lock", At: lockAt}
	if _, err := store.SaveOperationExecutionCheckpoint(ctx, "run-checkpoint", operation.StateAcquiringLock, "acquire_lock", operationrun.ExecutionSnapshot{}, nil, lockEntry, lockAt); err != nil {
		t.Fatal(err)
	}

	badAt := base.Add(3 * time.Second)
	badJournal := operation.JournalEntry{RunID: "run-checkpoint", Sequence: 3, State: operation.StateCreatingRestorePoint, Checkpoint: "restore_point", Message: "invalid snapshot", At: badAt}
	_, err = store.SaveOperationExecutionCheckpoint(ctx, "run-checkpoint", operation.StateCreatingRestorePoint, "restore_point",
		operationrun.ExecutionSnapshot{Verification: &operation.Verification{Passed: true}}, nil, badJournal, badAt)
	if err == nil {
		t.Fatal("invalid execution snapshot must fail atomically")
	}
	after, getErr := store.GetOperationRun(ctx, "run-checkpoint")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if after.Status.State != operation.StateAcquiringLock || after.Status.Checkpoint != "acquire_lock" {
		t.Fatalf("failed checkpoint changed run=%#v", after.Status)
	}
	journal, err = store.List(ctx, "run-checkpoint")
	if err != nil || len(journal) != 2 {
		t.Fatalf("failed checkpoint changed journal=%#v err=%v", journal, err)
	}

	conflictAt := base.Add(4 * time.Second)
	conflict := lockEntry
	conflict.Message = "different sequence two fact"
	conflict.At = conflictAt
	_, err = store.SaveOperationExecutionCheckpoint(ctx, "run-checkpoint", operation.StateAcquiringLock, "acquire_lock", operationrun.ExecutionSnapshot{}, nil, conflict, conflictAt)
	if err == nil {
		t.Fatal("conflicting journal retry must fail atomically")
	}
	after, _ = store.GetOperationRun(ctx, "run-checkpoint")
	if after.Status.UpdatedAt.Equal(conflictAt) {
		t.Fatal("journal conflict updated durable run timestamp")
	}
}

func TestOperationExecutionCheckpointRejectsMismatchedJournalEnvelope(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 8, 30, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-envelope", "task-envelope")
	entry := operation.JournalEntry{RunID: "other-run", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "wrong run", At: base.Add(time.Second)}
	if _, err := store.SaveOperationExecutionCheckpoint(ctx, "run-envelope", operation.StateQueued, "confirmed", operationrun.ExecutionSnapshot{}, nil, entry, entry.At); err == nil {
		t.Fatal("mismatched journal envelope must fail")
	}
}
