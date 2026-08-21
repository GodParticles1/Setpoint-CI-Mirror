package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func TestLoadOperationRuntimeSnapshotReconstructsJournalAndLeaseAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	base := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	now := base
	store, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	prepareAwaitingOperationRun(t, store, base, "run-recovery", "task-recovery")

	entries := []operation.JournalEntry{
		{RunID: "run-recovery", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "queued", At: base.Add(time.Second)},
		{RunID: "run-recovery", Sequence: 2, State: operation.StateAcquiringLock, Checkpoint: "acquire_lock", Message: "acquiring lock", At: base.Add(2 * time.Second)},
	}
	for _, entry := range entries {
		if err := store.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	now = base.Add(2 * time.Second)
	lease, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-recovery", TTL: 5 * time.Minute, Resources: []operation.LockResource{{Key: "data_object|||clickhouse|db.events"}}})
	if err != nil {
		t.Fatal(err)
	}
	creatingAt := base.Add(3 * time.Second)
	if err := store.Append(ctx, operation.JournalEntry{RunID: "run-recovery", Sequence: 3, State: operation.StateCreatingRestorePoint, Checkpoint: "restore_point", Message: "creating restore point", At: creatingAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	now = base.Add(30 * time.Second)
	store, err = open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.LoadOperationRuntimeSnapshot(ctx, "run-recovery", now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status.State != operation.StateCreatingRestorePoint || snapshot.JournalTail == nil || snapshot.JournalTail.Sequence != 3 || snapshot.NextJournalSequence != 4 {
		t.Fatalf("runtime snapshot=%#v", snapshot)
	}
	if snapshot.Lease == nil || snapshot.Lease.ID != lease.ID || !snapshot.LeaseActive {
		t.Fatalf("recovered lease=%#v active=%v", snapshot.Lease, snapshot.LeaseActive)
	}
	if !snapshot.WriteBlockedUntilReconcile {
		t.Fatal("restore-point state must block write replay until reconciliation")
	}

	expiredAt := lease.ExpiresAt
	expired, err := store.LoadOperationRuntimeSnapshot(ctx, "run-recovery", expiredAt)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Lease == nil || expired.LeaseActive {
		t.Fatalf("expired lease snapshot=%#v", expired)
	}
	if !expired.WriteBlockedUntilReconcile {
		t.Fatal("expired ownership must not make an ambiguous execution state writable")
	}
}

func TestLoadOperationRuntimeSnapshotFailsClosedOnRunJournalDivergence(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-diverge", "task-diverge")
	entry := operation.JournalEntry{RunID: "run-diverge", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "queued", At: base.Add(time.Second)}
	if err := store.Append(ctx, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE operation_runs SET checkpoint='corrupt_checkpoint' WHERE id='run-diverge'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOperationRuntimeSnapshot(ctx, "run-diverge", base.Add(2*time.Second)); err == nil {
		t.Fatal("run/journal divergence must fail closed")
	}
}

func TestLoadOperationRuntimeSnapshotRequiresJournalForExecutionState(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-no-journal", "task-no-journal")
	if _, err := store.db.ExecContext(ctx, `UPDATE operation_runs SET state='queued', checkpoint='confirmed' WHERE id='run-no-journal'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOperationRuntimeSnapshot(ctx, "run-no-journal", base.Add(time.Second)); err == nil {
		t.Fatal("execution state without durable journal must fail closed")
	}
}
