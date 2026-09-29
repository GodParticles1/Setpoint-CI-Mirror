package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
)

func TestRollbackReconnectWaitCheckpointAndRollbackAtSurviveStoreReopen(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 7, 1, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := open(ctx, path, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	prepareAwaitingOperationRun(t, store, base, "run-rollback-reconnect", "task-rollback-reconnect")

	sequence := int64(1)
	advance := func(state operation.State, checkpoint string, snapshot operationrun.ExecutionSnapshot, at time.Time) {
		t.Helper()
		entry := operation.JournalEntry{RunID: "run-rollback-reconnect", Sequence: sequence, State: state, Checkpoint: checkpoint, Message: "durable rollback reconnect test checkpoint", At: at}
		if _, err := store.SaveOperationExecutionCheckpoint(ctx, entry.RunID, state, checkpoint, snapshot, nil, entry, at); err != nil {
			t.Fatalf("advance to %s/%s: %v", state, checkpoint, err)
		}
		sequence++
	}

	advance(operation.StateQueued, "confirmed", operationrun.ExecutionSnapshot{}, base.Add(time.Second))
	advance(operation.StateAcquiringLock, "lease_acquired", operationrun.ExecutionSnapshot{}, base.Add(2*time.Second))
	advance(operation.StateCreatingRestorePoint, "stage_0_create_restore_point_queued", operationrun.ExecutionSnapshot{}, base.Add(3*time.Second))
	advance(operation.StateRunning, "stage_0_apply_queued", operationrun.ExecutionSnapshot{}, base.Add(4*time.Second))
	advance(operation.StateVerifying, "stage_0_verify_queued", operationrun.ExecutionSnapshot{}, base.Add(5*time.Second))

	rollbackAt := base.Add(6 * time.Second)
	snapshot := operationrun.ExecutionSnapshot{Stages: []operationrun.StageExecutionSnapshot{
		{StageIndex: 0, StageID: "stage-a", ExecutorNodeID: "node-a", RestorePoint: &operation.RestorePoint{ID: "rp-a"}, Apply: &operation.ApplyResult{Changed: true, Checkpoint: "applied-a"}, Verification: &operation.Verification{Passed: true}, ApplyAt: base.Add(4 * time.Second), VerificationAt: base.Add(5 * time.Second)},
		{StageIndex: 1, StageID: "stage-b", ExecutorNodeID: "node-b", RestorePoint: &operation.RestorePoint{ID: "rp-b"}, Apply: &operation.ApplyResult{Changed: true, Checkpoint: "applied-b"}, Rollback: &operation.RollbackResult{Restored: true, Checkpoint: "rolled-back-b"}, ApplyAt: base.Add(4 * time.Second), RollbackAt: rollbackAt},
	}}
	advance(operation.StateRollingBack, "stage_1_rollback_reconnect_wait", snapshot, rollbackAt)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run, err := store.GetOperationRun(ctx, "run-rollback-reconnect")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status.State != operation.StateRollingBack || run.Status.Checkpoint != "stage_1_rollback_reconnect_wait" || run.Execution == nil || len(run.Execution.Stages) != 2 {
		t.Fatalf("reopened run lost rollback wait truth: %#v", run)
	}
	facts := run.Execution.Stages[1]
	if facts.ExecutorNodeID != "node-b" || facts.Rollback == nil || !facts.RollbackAt.Equal(rollbackAt) {
		t.Fatalf("reopened rollback facts=%#v", facts)
	}
	journal, err := store.List(ctx, run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) != int(sequence-1) || journal[len(journal)-1].State != operation.StateRollingBack || journal[len(journal)-1].Checkpoint != "stage_1_rollback_reconnect_wait" {
		t.Fatalf("reopened journal tail=%#v", journal)
	}
}
