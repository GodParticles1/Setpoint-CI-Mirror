package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
)

func TestOperationJournalIsOrderedIdempotentAndDurableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	base := time.Date(2026, 8, 17, 6, 0, 0, 0, time.UTC)
	store, err := open(ctx, path, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	prepareAwaitingOperationRun(t, store, base, "run-journal", "task-journal")

	entries := []operation.JournalEntry{
		{RunID: "run-journal", Sequence: 1, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", Message: "plan awaits confirmation", At: base.Add(time.Second)},
		{RunID: "run-journal", Sequence: 2, State: operation.StateQueued, Checkpoint: "confirmed", Message: "confirmed operation queued", At: base.Add(2 * time.Second), Evidence: []operation.EvidenceRef{{ID: "plan-digest", Kind: "digest", SHA256: "abc"}}},
		{RunID: "run-journal", Sequence: 3, State: operation.StateAcquiringLock, Checkpoint: "acquire_lock", Message: "acquiring operation lock", At: base.Add(3 * time.Second)},
	}
	for _, entry := range entries {
		if err := store.Append(ctx, entry); err != nil {
			t.Fatalf("append sequence %d: %v", entry.Sequence, err)
		}
	}
	if err := store.Append(ctx, entries[1]); err != nil {
		t.Fatalf("idempotent duplicate append: %v", err)
	}
	conflict := entries[1]
	conflict.Message = "different durable fact"
	if err := store.Append(ctx, conflict); err == nil {
		t.Fatal("conflicting duplicate journal sequence must fail")
	}
	if err := store.Append(ctx, operation.JournalEntry{RunID: "run-journal", Sequence: 5, State: operation.StateCreatingRestorePoint, Message: "skipped sequence", At: base.Add(5 * time.Second)}); err == nil {
		t.Fatal("journal sequence gap must fail")
	}
	if err := store.Append(ctx, operation.JournalEntry{RunID: "run-journal", Sequence: 4, State: operation.StateSucceeded, Message: "illegal state jump", At: base.Add(4 * time.Second)}); err == nil {
		t.Fatal("journal invalid state transition must fail")
	}

	listed, err := store.List(ctx, "run-journal")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || listed[0].Sequence != 1 || listed[2].Sequence != 3 || len(listed[1].Evidence) != 1 {
		t.Fatalf("journal=%#v", listed)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listed, err = store.List(ctx, "run-journal")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || listed[1].Checkpoint != "confirmed" || listed[1].Evidence[0].SHA256 != "abc" {
		t.Fatalf("restored journal=%#v", listed)
	}
}

func TestOperationJournalRequiresExistingRun(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := operation.JournalEntry{RunID: "missing", Sequence: 1, State: operation.StateDraft, Message: "missing run", At: time.Now().UTC()}
	if err := store.Append(context.Background(), entry); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("append missing run error=%v", err)
	}
	if _, err := store.List(context.Background(), "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("list missing run error=%v", err)
	}
}
