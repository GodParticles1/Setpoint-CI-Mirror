package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestTaskLifecycleIsTransactionalIdempotentAndDurable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	now := time.Date(2026, 8, 3, 17, 0, 0, 0, time.UTC)
	store, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	seedTaskDependencies(t, store, "agent-task", "test.read-only", now)

	wanted := taskResource("task-1", "idem-1", "agent-task", "test.read-only", now)
	created, wasCreated, err := store.CreateTask(ctx, wanted)
	if err != nil || !wasCreated || created.Status.Phase != task.PhasePending {
		t.Fatalf("create task=%#v created=%v err=%v", created, wasCreated, err)
	}
	duplicate, wasCreated, err := store.CreateTask(ctx, wanted)
	if err != nil || wasCreated || duplicate.Metadata.ID != wanted.Metadata.ID {
		t.Fatalf("duplicate create=%#v created=%v err=%v", duplicate, wasCreated, err)
	}
	conflict := wanted
	conflict.Metadata.ID = "task-conflict"
	conflict.Spec.PluginID = "other-plugin"
	if _, _, err := store.CreateTask(ctx, conflict); !errors.Is(err, task.ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error=%v", err)
	}

	now = now.Add(time.Second)
	claimed, err := store.ClaimTask(ctx, "agent-task", "claim-1", now)
	if err != nil || claimed == nil || claimed.Status.Phase != task.PhaseClaimed || claimed.Status.ClaimID != "claim-1" || claimed.Status.Attempt != 1 {
		t.Fatalf("claim task=%#v err=%v", claimed, err)
	}
	recovered, err := store.ClaimTask(ctx, "agent-task", "claim-must-not-replace", now.Add(time.Second))
	if err != nil || recovered == nil || recovered.Status.ClaimID != "claim-1" || recovered.Status.Attempt != 1 {
		t.Fatalf("recover claim=%#v err=%v", recovered, err)
	}

	now = now.Add(2 * time.Second)
	running, err := store.AcknowledgeTask(ctx, "agent-task", "task-1", "claim-1", now)
	if err != nil || running.Status.Phase != task.PhaseRunning || running.Status.AcknowledgedAt == nil {
		t.Fatalf("acknowledge task=%#v err=%v", running, err)
	}
	if _, err := store.AcknowledgeTask(ctx, "agent-task", "task-1", "wrong-claim", now); !errors.Is(err, task.ErrClaimMismatch) {
		t.Fatalf("wrong claim acknowledgement error=%v", err)
	}

	now = now.Add(time.Second)
	canceling, err := store.CancelTask(ctx, "task-1", now)
	if err != nil || canceling.Status.Phase != task.PhaseCancelRequested || canceling.Status.CompletedAt != nil {
		t.Fatalf("cancel running task=%#v err=%v", canceling, err)
	}
	result := checkResult("test.read-only", now)
	result.Items = append(result.Items, task.CheckItem{
		ID: "manual-item", Status: task.ItemManualReview, Name: "Manual item",
		CurrentValue: "policy dependent", RecommendedValue: "confirm policy", Applicable: true,
		ReviewReason: "the target policy is not configured", ExecutedAt: now,
	})
	submission := task.ResultSubmission{ClaimID: "claim-1", Phase: task.PhaseCanceled, Result: &result}
	now = now.Add(time.Second)
	completed, err := store.CompleteTask(ctx, "agent-task", "task-1", submission, now)
	if err != nil || completed.Status.Phase != task.PhaseCanceled || completed.Result == nil || completed.Result.PluginID != "test.read-only" {
		t.Fatalf("complete task=%#v err=%v", completed, err)
	}
	if _, err := store.CompleteTask(ctx, "agent-task", "task-1", submission, now.Add(time.Second)); err != nil {
		t.Fatalf("repeat identical result: %v", err)
	}
	conflictingSubmission := submission
	conflictingSubmission.Result.PluginVersion = "different"
	if _, err := store.CompleteTask(ctx, "agent-task", "task-1", conflictingSubmission, now.Add(time.Second)); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("conflicting result error=%v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restored, err := store.GetTask(ctx, "task-1")
	if err != nil || restored.Status.Phase != task.PhaseCanceled || restored.Result == nil {
		t.Fatalf("restored task=%#v err=%v", restored, err)
	}
	if len(restored.Result.Items) != 2 || restored.Result.Items[1].Status != task.ItemManualReview ||
		restored.Result.Items[1].ReviewReason != "the target policy is not configured" {
		t.Fatalf("manual review result was not restored: %#v", restored.Result)
	}
	listed, err := store.ListTasks(ctx)
	if err != nil || len(listed) != 1 || listed[0].Result == nil {
		t.Fatalf("listed tasks=%#v err=%v", listed, err)
	}
	var events int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = 'task-1'`).Scan(&events); err != nil || events != 5 {
		t.Fatalf("task events=%d err=%v", events, err)
	}
}

func TestPendingTaskCancellationAndEmptyClaim(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 18, 0, 0, 0, time.UTC)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedTaskDependencies(t, store, "agent-task", "test.read-only", now)
	resource := taskResource("task-cancel", "idem-cancel", "agent-task", "test.read-only", now)
	if _, _, err := store.CreateTask(ctx, resource); err != nil {
		t.Fatal(err)
	}
	canceled, err := store.CancelTask(ctx, resource.Metadata.ID, now.Add(time.Second))
	if err != nil || canceled.Status.Phase != task.PhaseCanceled || canceled.Status.CompletedAt == nil {
		t.Fatalf("pending cancellation=%#v err=%v", canceled, err)
	}
	claimed, err := store.ClaimTask(ctx, "agent-task", "claim-unused", now.Add(2*time.Second))
	if err != nil || claimed != nil {
		t.Fatalf("empty claim=%#v err=%v", claimed, err)
	}
}

func seedTaskDependencies(t *testing.T, store *Store, agentID, pluginID string, now time.Time) {
	t.Helper()
	if _, err := store.RegisterNode(context.Background(), domain.Registration{
		AgentID: agentID, Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64",
		AgentVersion: "test", ReceivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCheck(context.Background(), plugin.Metadata{
		ID: pluginID, Name: "Read only", Version: "1", Description: "test",
		Mode: plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "none",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{},
	}); err != nil {
		t.Fatal(err)
	}
}

func taskResource(id, idempotencyKey, agentID, pluginID string, now time.Time) task.Resource {
	return task.Resource{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckTask",
		Metadata: task.Metadata{ID: id, IdempotencyKey: idempotencyKey, CreatedAt: now},
		Spec:     task.Spec{NodeID: agentID, PluginID: pluginID, Parameters: json.RawMessage(`{}`)},
		Status:   task.Status{Phase: task.PhasePending, UpdatedAt: now},
	}
}

func checkResult(pluginID string, now time.Time) task.CheckResult {
	compliant := true
	return task.CheckResult{
		PluginID: pluginID, PluginVersion: "1", State: task.CheckCompleted,
		StartedAt: now, CompletedAt: now.Add(time.Millisecond),
		Items: []task.CheckItem{{
			ID: "item", Name: "Item", CurrentValue: "0", RecommendedValue: "0", Compliant: &compliant,
			Risk: "low", RiskDescription: "test", EvidenceSummary: "read-only evidence", Applicable: true,
			ExecutedAt: now,
		}},
	}
}
