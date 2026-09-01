package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestOperationExecutionSnapshotIsDurableAndMonotonicAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	base := time.Date(2026, 8, 17, 3, 30, 0, 0, time.UTC)
	store, err := open(ctx, path, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	prepareAwaitingOperationRun(t, store, base, "run-execution", "task-execution")

	target := operation.Target{Kind: operation.TargetNode, NodeID: "node-execution"}
	point := operation.RestorePoint{
		ID: "restore-1", ProviderID: "test.restore", OperationID: "operation.test", RunID: "run-execution",
		Status: operation.RestorePointVerified, Targets: []operation.Target{target}, CreatedAt: base,
		Manifest: operation.Artifact{SchemaVersion: "test.restore.v1", Payload: json.RawMessage(`{"owned":true}`)},
	}
	apply := operation.ApplyResult{Changed: true, Checkpoint: "apply_complete", State: operation.Artifact{SchemaVersion: "test.apply.v1", Payload: json.RawMessage(`{"changed":true}`)}}
	verify := operation.Verification{Passed: false, Summary: "verification requested rollback"}
	rollback := operation.RollbackResult{Restored: true, Checkpoint: "rollback_complete", State: operation.Artifact{SchemaVersion: "test.rollback.v1", Payload: json.RawMessage(`{"restored":true}`)}}
	rollbackVerify := operation.Verification{Passed: true, Summary: "rollback verified"}

	steps := []struct {
		state      operation.State
		checkpoint string
		snapshot   operationrun.ExecutionSnapshot
	}{
		{operation.StateQueued, "confirmed", operationrun.ExecutionSnapshot{}},
		{operation.StateAcquiringLock, "acquire_lock", operationrun.ExecutionSnapshot{}},
		{operation.StateCreatingRestorePoint, "restore_point", operationrun.ExecutionSnapshot{RestorePoint: &point}},
		{operation.StateRunning, "apply", operationrun.ExecutionSnapshot{Apply: &apply}},
		{operation.StateVerifying, "verify", operationrun.ExecutionSnapshot{Verification: &verify}},
		{operation.StateRollingBack, "rollback", operationrun.ExecutionSnapshot{Rollback: &rollback}},
		{operation.StateRollingBack, "rollback_verified", operationrun.ExecutionSnapshot{RollbackVerification: &rollbackVerify}},
		{operation.StateRolledBack, "complete_rollback", operationrun.ExecutionSnapshot{}},
	}
	for index, step := range steps {
		at := base.Add(time.Duration(index+1) * time.Second)
		entry := operation.JournalEntry{RunID: "run-execution", Sequence: int64(index + 1), State: step.state, Checkpoint: step.checkpoint, Message: "test execution checkpoint", At: at}
		if err := store.Append(ctx, entry); err != nil {
			t.Fatalf("journal step %d state=%s: %v", index, step.state, err)
		}
		stored, err := store.SaveOperationExecutionSnapshot(ctx, "run-execution", step.state, step.checkpoint, step.snapshot, nil, at)
		if err != nil {
			t.Fatalf("snapshot step %d state=%s: %v", index, step.state, err)
		}
		if stored.Status.ApplyAvailable {
			t.Fatal("durable execution storage must not enable Product Apply")
		}
	}

	stored, err := store.GetOperationRun(ctx, "run-execution")
	if err != nil {
		t.Fatal(err)
	}
	assertCompleteExecutionSnapshot(t, stored)
	if stored.Status.State != operation.StateRolledBack {
		t.Fatalf("state=%s", stored.Status.State)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.GetOperationRun(ctx, "run-execution")
	if err != nil {
		t.Fatal(err)
	}
	assertCompleteExecutionSnapshot(t, restored)
	if restored.Status.State != operation.StateRolledBack || restored.Status.Checkpoint != "complete_rollback" {
		t.Fatalf("restored status=%#v", restored.Status)
	}

	before := restored
	_, err = store.SaveOperationExecutionSnapshot(ctx, "run-execution", operation.StateRunning, "stale_running", operationrun.ExecutionSnapshot{}, nil, base.Add(time.Minute))
	if err == nil {
		t.Fatal("execution snapshot must not bypass the durable journal state")
	}
	after, getErr := store.GetOperationRun(ctx, "run-execution")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if after.Status.State != before.Status.State || after.Status.Checkpoint != before.Status.Checkpoint || after.Execution == nil || after.Execution.RollbackVerification == nil {
		t.Fatalf("rejected stale update changed durable run: before=%#v after=%#v", before, after)
	}
}

func TestOperationExecutionSnapshotRejectsOutOfOrderFacts(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepareAwaitingOperationRun(t, store, base, "run-order", "task-order")

	at := base.Add(time.Second)
	if err := store.Append(ctx, operation.JournalEntry{RunID: "run-order", Sequence: 1, State: operation.StateQueued, Checkpoint: "confirmed", Message: "queued", At: at}); err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveOperationExecutionSnapshot(ctx, "run-order", operation.StateQueued, "confirmed",
		operationrun.ExecutionSnapshot{Verification: &operation.Verification{Passed: true}}, nil, at)
	if err == nil {
		t.Fatal("verification without Apply must fail closed")
	}
	stored, getErr := store.GetOperationRun(ctx, "run-order")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.Status.State != operation.StateQueued || stored.Status.Checkpoint != "confirmed" || stored.Execution != nil {
		t.Fatalf("invalid execution facts mutated run: %#v", stored)
	}
}

func prepareAwaitingOperationRun(t *testing.T, store *Store, now time.Time, runID, taskID string) {
	prepareAwaitingOperationRunWithSecretRefs(t, store, now, runID, taskID, nil)
}

func prepareAwaitingOperationRunWithSecretRefs(t *testing.T, store *Store, now time.Time, runID, taskID string, secretRefs []operation.SecretRef) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "node-execution", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	spec := operationrun.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap", NodeID: "node-execution", Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-execution"}}, Parameters: json.RawMessage(`{}`), SecretRefs: secretRefs}
	run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun", Metadata: operationrun.Metadata{ID: runID, IdempotencyKey: runID + "-idem", CreatedAt: now}, Spec: spec, Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued", TaskID: taskID, UpdatedAt: now}}
	planningTask := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: taskID, IdempotencyKey: runID + ":planning", CreatedAt: now}, Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, Targets: spec.Targets, Parameters: spec.Parameters, SecretRefs: secretRefs}, Status: task.Status{Phase: task.PhasePending, UpdatedAt: now}}
	if _, _, err := store.CreateOperationRun(ctx, run, planningTask); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTask(ctx, spec.NodeID, "claim-"+runID, now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := store.AcknowledgeTask(ctx, spec.NodeID, taskID, "claim-"+runID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	plan := operation.Plan{SchemaVersion: "test.plan.v1", Summary: "plan", Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}
	impact := operation.Impact{Summary: "impact", Risk: operation.RiskHigh}
	result := operation.PlanningResult{OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", StartedAt: now, CompletedAt: now.Add(3 * time.Second), Plan: &plan, Impact: &impact, PlanDigest: "sha256:plan"}
	if _, err := store.CompleteTask(ctx, spec.NodeID, taskID, task.ResultSubmission{ClaimID: "claim-" + runID, Phase: task.PhaseSucceeded, OperationResult: &result}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func assertCompleteExecutionSnapshot(t *testing.T, run operationrun.Resource) {
	t.Helper()
	if run.Execution == nil || run.Execution.RestorePoint == nil || run.Execution.Apply == nil || run.Execution.Verification == nil || run.Execution.Rollback == nil || run.Execution.RollbackVerification == nil {
		t.Fatalf("execution snapshot incomplete: %#v", run.Execution)
	}
	if run.Execution.RestorePoint.ID != "restore-1" || !run.Execution.Apply.Changed || run.Execution.Verification.Passed || !run.Execution.Rollback.Restored || !run.Execution.RollbackVerification.Passed {
		t.Fatalf("execution snapshot values changed: %#v", run.Execution)
	}
}
