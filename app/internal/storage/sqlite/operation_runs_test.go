package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestOperationRunPlanningResultIsAtomicAndDurableAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	store, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "node-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	spec := operationrun.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap", NodeID: "node-1", Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}, Parameters: json.RawMessage(`{}`)}
	run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun", Metadata: operationrun.Metadata{ID: "run-1", IdempotencyKey: "run-idem", CreatedAt: now}, Spec: spec, Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued", TaskID: "task-op-1", UpdatedAt: now}}
	planningTask := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: "task-op-1", IdempotencyKey: "run-1:planning", CreatedAt: now}, Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, Targets: spec.Targets, Parameters: spec.Parameters}, Status: task.Status{Phase: task.PhasePending, UpdatedAt: now}}
	created, wasCreated, err := store.CreateOperationRun(ctx, run, planningTask)
	if err != nil || !wasCreated || created.Status.TaskID != planningTask.Metadata.ID {
		t.Fatalf("created=%#v wasCreated=%v err=%v", created, wasCreated, err)
	}
	claimed, err := store.ClaimTask(ctx, "node-1", "claim-op", now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-1", claimed.Metadata.ID, "claim-op", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	plan := operation.Plan{SchemaVersion: "test.v1", Summary: "plan", Steps: []operation.PlanStep{}, Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}
	impact := operation.Impact{Summary: "impact", Risk: operation.RiskHigh, Changes: []operation.Change{}}
	result := operation.PlanningResult{OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", StartedAt: now, CompletedAt: now.Add(3 * time.Second), Plan: &plan, Impact: &impact, PlanDigest: "sha256:plan"}
	completed, err := store.CompleteTask(ctx, "node-1", claimed.Metadata.ID, task.ResultSubmission{ClaimID: "claim-op", Phase: task.PhaseSucceeded, OperationResult: &result}, now.Add(3*time.Second))
	if err != nil || completed.OperationResult == nil {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	stored, err := store.GetOperationRun(ctx, "run-1")
	if err != nil || stored.Status.State != operation.StateAwaitingConfirm || stored.PlanDigest != "sha256:plan" || stored.Plan == nil {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.GetOperationRun(ctx, "run-1")
	if err != nil || restored.Status.State != operation.StateAwaitingConfirm || restored.PlanDigest != "sha256:plan" || restored.Impact == nil {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
}

func TestCanceledOperationRunRejectsLateSuccessfulPlanningResult(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "node-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	spec := operationrun.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap", NodeID: "node-1", Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}, Parameters: json.RawMessage(`{}`)}
	run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun", Metadata: operationrun.Metadata{ID: "run-cancel", IdempotencyKey: "run-cancel-idem", CreatedAt: now}, Spec: spec, Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued", TaskID: "task-cancel", UpdatedAt: now}}
	planningTask := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: "task-cancel", IdempotencyKey: "run-cancel:planning", CreatedAt: now}, Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, Targets: spec.Targets, Parameters: spec.Parameters}, Status: task.Status{Phase: task.PhasePending, UpdatedAt: now}}
	if _, _, err := store.CreateOperationRun(ctx, run, planningTask); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTask(ctx, "node-1", "claim-cancel", now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-1", claimed.Metadata.ID, "claim-cancel", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelOperationRun(ctx, run.Metadata.ID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	result := operation.PlanningResult{OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", StartedAt: now, CompletedAt: now.Add(4 * time.Second)}
	_, err = store.CompleteTask(ctx, "node-1", claimed.Metadata.ID, task.ResultSubmission{ClaimID: "claim-cancel", Phase: task.PhaseSucceeded, OperationResult: &result}, now.Add(4*time.Second))
	if !errors.Is(err, task.ErrInvalidTransition) {
		t.Fatalf("late success error=%v", err)
	}
	stored, err := store.GetOperationRun(ctx, run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.State != operation.StateCanceledBeforeApply || stored.Plan != nil || stored.PlanDigest != "" {
		t.Fatalf("canceled run overwritten: %#v", stored)
	}
}

func TestBlockedOperationRunKeepsAbsentPlanningArtifactsNil(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 11, 30, 0, 0, time.UTC)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "node-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	spec := operationrun.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap", NodeID: "node-1", Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}, Parameters: json.RawMessage(`{}`)}
	run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun", Metadata: operationrun.Metadata{ID: "run-blocked", IdempotencyKey: "run-blocked-idem", CreatedAt: now}, Spec: spec, Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued", TaskID: "task-blocked", UpdatedAt: now}}
	planningTask := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: "task-blocked", IdempotencyKey: "run-blocked:planning", CreatedAt: now}, Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, Targets: spec.Targets, Parameters: spec.Parameters}, Status: task.Status{Phase: task.PhasePending, UpdatedAt: now}}
	if _, _, err := store.CreateOperationRun(ctx, run, planningTask); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTask(ctx, "node-1", "claim-blocked", now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-1", claimed.Metadata.ID, "claim-blocked", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	result := operation.PlanningResult{OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest,
		State: operation.StateBlocked, Checkpoint: "secret_delivery_unavailable", StartedAt: now, CompletedAt: now.Add(3 * time.Second),
		Block: &operation.Block{Code: "secret_delivery_unavailable", Message: "blocked", SafeNext: "manual_review", ManualReview: true}}
	if _, err := store.CompleteTask(ctx, "node-1", claimed.Metadata.ID, task.ResultSubmission{ClaimID: "claim-blocked", Phase: task.PhaseSucceeded, OperationResult: &result}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetOperationRun(ctx, run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Discovery != nil || stored.Precheck != nil || stored.Plan != nil || stored.Impact != nil {
		t.Fatalf("absent artifacts materialized as empty values: %#v", stored)
	}
}

func TestOperationRunRecoveryCodesSerializeWithoutHTTPStateTranslation(t *testing.T) {
	for _, code := range []string{"commit_unknown", "rollback_pending", "ownership_mismatch", "fingerprint_drift"} {
		resource := operationrun.Resource{Status: operationrun.Status{State: operation.StateInterrupted, Recovery: &operationrun.Recovery{Code: code, SafeNext: "manual_review", ManualReview: true}}}
		encoded, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(encoded) || !strings.Contains(string(encoded), `"code":"`+code+`"`) {
			t.Fatalf("code=%s json=%s", code, encoded)
		}
	}
}
