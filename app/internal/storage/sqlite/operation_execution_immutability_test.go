package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
)

func TestOperationExecutionSnapshotRejectsRewriteOfDurableFacts(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-immutable", "task-immutable")

	entries := []operation.JournalEntry{
		{RunID: "run-immutable", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "queued", At: base.Add(time.Second)},
		{RunID: "run-immutable", Sequence: 2, State: operation.StateAcquiringLock, Checkpoint: "acquire_lock", Message: "acquiring lock", At: base.Add(2 * time.Second)},
		{RunID: "run-immutable", Sequence: 3, State: operation.StateCreatingRestorePoint, Checkpoint: "restore_point", Message: "creating restore point", At: base.Add(3 * time.Second)},
	}
	for _, entry := range entries {
		if err := store.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	point := operation.RestorePoint{ID: "restore-immutable", ProviderID: "test.restore", OperationID: "operation.test", RunID: "run-immutable", Status: operation.RestorePointVerified, Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-execution"}}, CreatedAt: base, Manifest: operation.Artifact{SchemaVersion: "test.restore.v1", Payload: json.RawMessage(`{"owned":true}`)}}
	if _, err := store.SaveOperationExecutionSnapshot(ctx, "run-immutable", operation.StateCreatingRestorePoint, "restore_point", operationrun.ExecutionSnapshot{RestorePoint: &point}, nil, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, operation.JournalEntry{RunID: "run-immutable", Sequence: 4, State: operation.StateRunning, Checkpoint: "apply", Message: "running apply", At: base.Add(4 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	apply := operation.ApplyResult{Changed: true, Checkpoint: "apply-one", State: operation.Artifact{SchemaVersion: "test.apply.v1", Payload: json.RawMessage(`{"version":1}`)}}
	if _, err := store.SaveOperationExecutionSnapshot(ctx, "run-immutable", operation.StateRunning, "apply", operationrun.ExecutionSnapshot{Apply: &apply}, nil, base.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}

	rewritten := apply
	rewritten.Checkpoint = "apply-two"
	rewritten.State.Payload = json.RawMessage(`{"version":2}`)
	if _, err := store.SaveOperationExecutionSnapshot(ctx, "run-immutable", operation.StateRunning, "apply", operationrun.ExecutionSnapshot{Apply: &rewritten}, nil, base.Add(5*time.Second)); err == nil {
		t.Fatal("different durable Apply result must not overwrite persisted fact")
	}
	stored, err := store.GetOperationRun(ctx, "run-immutable")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Execution == nil || stored.Execution.Apply == nil || stored.Execution.Apply.Checkpoint != "apply-one" || string(stored.Execution.Apply.State.Payload) != `{"version":1}` {
		t.Fatalf("durable Apply fact changed: %#v", stored.Execution)
	}
	if _, err := store.SaveOperationExecutionSnapshot(ctx, "run-immutable", operation.StateRunning, "apply", operationrun.ExecutionSnapshot{Apply: &apply}, nil, base.Add(6*time.Second)); err != nil {
		t.Fatalf("same durable Apply fact should be idempotent: %v", err)
	}
}
