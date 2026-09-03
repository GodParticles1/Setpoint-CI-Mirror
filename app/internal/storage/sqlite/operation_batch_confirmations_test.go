package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationbatch"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestOperationBatchConfirmationReceiptIsDurableAndReconstructable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedBatchReceiptForeignKeys(t, ctx, store, now, "run-a", "run-b")
	frozen := []operationbatch.FrozenMember{
		{Identity: operationbatch.MemberIdentity{TaskID: "check-task-a", CheckID: "check-a", NodeID: "node-1"}, RunID: "run-a", PlanDigest: "sha256:plan-a"},
		{Identity: operationbatch.MemberIdentity{TaskID: "check-task-b", CheckID: "check-b", NodeID: "node-1"}, RunID: "run-b", PlanDigest: "sha256:plan-b"},
	}
	receipt, err := operationbatch.NewReceipt("batch-1", "check-run-1", "batch-confirm-1", frozen, now)
	if err != nil {
		t.Fatal(err)
	}
	created, wasCreated, err := store.CreateOrGetOperationBatchConfirmation(ctx, receipt)
	if err != nil || !wasCreated || len(created.Members) != 2 {
		t.Fatalf("created=%#v wasCreated=%v err=%v", created, wasCreated, err)
	}
	duplicate, wasCreated, err := store.CreateOrGetOperationBatchConfirmation(ctx, receipt)
	if err != nil || wasCreated || duplicate.ConfirmationFingerprint != receipt.ConfirmationFingerprint {
		t.Fatalf("duplicate=%#v wasCreated=%v err=%v", duplicate, wasCreated, err)
	}
	if _, err := store.UpdateOperationBatchConfirmationMemberState(ctx, receipt.BatchID, 0, operationbatch.MemberConfirmed, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.GetOperationBatchConfirmation(ctx, receipt.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Members[0].State != operationbatch.MemberConfirmed || restored.Members[1].State != operationbatch.MemberPending || restored.Members[1].PlanDigest != "sha256:plan-b" {
		t.Fatalf("restored=%#v", restored)
	}
	listed, err := store.ListOperationBatchConfirmationsByCheckRun(ctx, "check-run-1", 50, 0)
	if err != nil || len(listed) != 1 || listed[0].BatchID != receipt.BatchID {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	pending, err := store.ListPendingOperationBatchConfirmations(ctx, 50, 0)
	if err != nil || len(pending) != 1 || pending[0].Members[1].State != operationbatch.MemberPending {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
}

func TestOperationBatchConfirmationRejectsSameKeyChangedFingerprint(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedBatchReceiptForeignKeys(t, ctx, store, now, "run-a", "run-b")
	first, _ := operationbatch.NewReceipt("batch-1", "check-run-1", "batch-confirm-1", []operationbatch.FrozenMember{{Identity: operationbatch.MemberIdentity{TaskID: "check-task-a", CheckID: "check-a", NodeID: "node-1"}, RunID: "run-a", PlanDigest: "sha256:plan-a"}}, now)
	if _, _, err := store.CreateOrGetOperationBatchConfirmation(ctx, first); err != nil {
		t.Fatal(err)
	}
	changed, _ := operationbatch.NewReceipt("batch-2", "check-run-1", "batch-confirm-1", []operationbatch.FrozenMember{{Identity: operationbatch.MemberIdentity{TaskID: "check-task-b", CheckID: "check-b", NodeID: "node-1"}, RunID: "run-b", PlanDigest: "sha256:plan-b"}}, now)
	if _, _, err := store.CreateOrGetOperationBatchConfirmation(ctx, changed); !errors.Is(err, operationbatch.ErrFingerprintConflict) {
		t.Fatalf("conflict=%v", err)
	}
}

func seedBatchReceiptForeignKeys(t *testing.T, ctx context.Context, store *Store, now time.Time, runIDs ...string) {
	t.Helper()
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "node-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	check := checkrun.Resource{APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun", Metadata: checkrun.Metadata{ID: "check-run-1", IdempotencyKey: "check-run-idem", Name: "batch source", CreatedAt: now}, Spec: checkrun.Spec{NodeIDs: []string{"node-1"}, CheckIDs: []string{"check-a", "check-b"}, Parameters: map[string]json.RawMessage{}}}
	if _, _, err := store.CreateCheckRun(ctx, check, nil); err != nil {
		t.Fatal(err)
	}
	for index, runID := range runIDs {
		spec := operationrun.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap", NodeID: "node-1", Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}, Parameters: json.RawMessage(`{}`)}
		taskID := runID + ":planning"
		run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun", Metadata: operationrun.Metadata{ID: runID, IdempotencyKey: runID + "-idem", CreatedAt: now.Add(time.Duration(index) * time.Second)}, Spec: spec, Status: operationrun.Status{State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", TaskID: taskID, UpdatedAt: now}}
		planning := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: taskID, IdempotencyKey: runID + ":planning", CreatedAt: now}, Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion, CapabilityDigest: spec.CapabilityDigest, Targets: spec.Targets, Parameters: spec.Parameters}, Status: task.Status{Phase: task.PhaseSucceeded, UpdatedAt: now}}
		if _, _, err := store.CreateOperationRun(ctx, run, planning); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE operation_runs SET plan_digest=? WHERE id=?`, "sha256:plan-"+string(rune('a'+index)), runID); err != nil {
			t.Fatal(err)
		}
	}
}
