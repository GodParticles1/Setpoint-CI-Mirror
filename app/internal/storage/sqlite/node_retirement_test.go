package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/auth"
	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestRetireNodeRevokesAuthorityAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedTaskDependencies(t, store, "retire-node", "retire.check", now)
	if err := store.RecordHeartbeat(ctx, "retire-node", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	credential := mustToken(t, auth.AgentCredential)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_credentials
		(id,agent_id,secret_digest,created_at) VALUES(?,?,?,?)`,
		credential.ID, "retire-node", credential.Digest, formatTime(now)); err != nil {
		t.Fatal(err)
	}
	secondCredential := mustToken(t, auth.AgentCredential)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_credentials
		(id,agent_id,secret_digest,created_at) VALUES(?,?,?,?)`,
		secondCredential.ID, "retire-node", secondCredential.Digest, formatTime(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	presented := mustParse(t, auth.AgentCredential, credential.Secret)

	run := checkrun.Resource{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: checkrun.Metadata{ID: "retire-check-run", IdempotencyKey: "retire-check-run-idem", Name: "history", CreatedAt: now},
		Spec: checkrun.Spec{NodeIDs: []string{"retire-node"}, CheckIDs: []string{"retire.check"},
			Parameters: map[string]json.RawMessage{"retire.check": json.RawMessage(`{}`)}},
	}
	checkTask := taskResource("retire-check-task", "retire-check-task-idem", "retire-node", "retire.check", now)
	if _, created, err := store.CreateCheckRun(ctx, run, []task.Resource{checkTask}); err != nil || !created {
		t.Fatalf("create historical check run: created=%v err=%v", created, err)
	}
	claimed, err := store.ClaimTask(ctx, "retire-node", "retire-check-claim", now.Add(2*time.Second))
	if err != nil || claimed == nil {
		t.Fatalf("claim historical check task: %#v %v", claimed, err)
	}
	if _, err := store.AcknowledgeTask(ctx, "retire-node", claimed.Metadata.ID, claimed.Status.ClaimID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	result := checkResult("retire.check", now.Add(3*time.Second))
	if _, err := store.CompleteTask(ctx, "retire-node", claimed.Metadata.ID,
		task.ResultSubmission{ClaimID: claimed.Status.ClaimID, Phase: task.PhaseSucceeded, Result: &result}, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}

	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "retire-node"}}
	opSpec := operationrun.Spec{OperationID: "operation.history", OperationVersion: "1.0.0", CapabilityDigest: "sha256:history",
		NodeID: "retire-node", Targets: targets, Parameters: json.RawMessage(`{}`)}
	opTask := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask,
		Metadata: task.Metadata{ID: "retire-operation-task", IdempotencyKey: "retire-operation-task-idem", CreatedAt: now},
		Spec: task.Spec{NodeID: "retire-node", OperationID: opSpec.OperationID, OperationVersion: opSpec.OperationVersion,
			CapabilityDigest: opSpec.CapabilityDigest, Targets: targets, Parameters: json.RawMessage(`{}`)},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: now}}
	opRun := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun",
		Metadata: operationrun.Metadata{ID: "retire-operation-run", IdempotencyKey: "retire-operation-run-idem", CreatedAt: now},
		Spec:     opSpec, Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued",
			TaskID: opTask.Metadata.ID, UpdatedAt: now}}
	if _, created, err := store.CreateOperationRun(ctx, opRun, opTask); err != nil || !created {
		t.Fatalf("create historical operation run: created=%v err=%v", created, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET phase='succeeded',completed_at=?,updated_at=? WHERE id=?`,
		formatTime(now.Add(5*time.Second)), formatTime(now.Add(5*time.Second)), opTask.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE operation_runs SET state='blocked',checkpoint='historical',updated_at=? WHERE id=?`,
		formatTime(now.Add(5*time.Second)), opRun.Metadata.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.RetireNode(ctx, "retire-node", now.Add(6*time.Second)); err != nil {
		t.Fatalf("retire node: %v", err)
	}
	if nodes, err := store.ListNodes(ctx, time.Minute); err != nil || len(nodes) != 0 {
		t.Fatalf("active nodes after retirement=%#v err=%v", nodes, err)
	}
	if _, err := store.GetNode(ctx, "retire-node", time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("retired node remained active: %v", err)
	}
	if _, err := store.AuthenticateAgentCredential(ctx, presented, "retire-node", now.Add(7*time.Second)); authCode(err) != auth.CodeRevoked {
		t.Fatalf("old credential authority survived retirement: %v", err)
	}
	if _, err := store.AuthenticateAgentCredential(ctx, mustParse(t, auth.AgentCredential, secondCredential.Secret),
		"retire-node", now.Add(7*time.Second)); authCode(err) != auth.CodeRevoked {
		t.Fatalf("second active credential authority survived retirement: %v", err)
	}
	if err := store.RecordHeartbeat(ctx, "retire-node", now.Add(7*time.Second)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("retired heartbeat accepted: %v", err)
	}
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "retire-node", Hostname: "retry", OS: "linux",
		OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now.Add(7 * time.Second)}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("retired registration reactivated node: %v", err)
	}
	if _, _, err := store.CreateTask(ctx, taskResource("after-retire", "after-retire-idem", "retire-node", "retire.check", now.Add(7*time.Second))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("task created for retired node: %v", err)
	}
	if _, err := store.GetCheckRun(ctx, run.Metadata.ID); err != nil {
		t.Fatalf("check history lost: %v", err)
	}
	if _, err := store.GetTask(ctx, checkTask.Metadata.ID); err != nil {
		t.Fatalf("task history lost: %v", err)
	}
	if _, err := store.GetOperationRun(ctx, opRun.Metadata.ID); err != nil {
		t.Fatalf("operation history lost: %v", err)
	}
	for table, want := range map[string]int{"agent_registrations": 1, "agent_heartbeats": 1, "tasks": 2,
		"task_results": 1, "check_runs": 1, "operation_runs": 1} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s history count=%d want=%d err=%v", table, count, want, err)
		}
	}
	if err := store.RetireNode(ctx, "retire-node", now.Add(8*time.Second)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("repeated retirement=%v", err)
	}
	assertForeignKeysClean(t, ctx, store.db)
}

func TestRetireNodeBlocksEveryNonTerminalTaskPhase(t *testing.T) {
	for _, phase := range []task.Phase{task.PhasePending, task.PhaseClaimed, task.PhaseRunning, task.PhaseCancelRequested} {
		phase := phase
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
			store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedTaskDependencies(t, store, "active-node", "active.check", now)
			credential := mustToken(t, auth.AgentCredential)
			if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_credentials
				(id,agent_id,secret_digest,created_at) VALUES(?,?,?,?)`,
				credential.ID, "active-node", credential.Digest, formatTime(now)); err != nil {
				t.Fatal(err)
			}
			resource := taskResource("active-task", "active-task-idem", "active-node", "active.check", now)
			if _, _, err := store.CreateTask(ctx, resource); err != nil {
				t.Fatal(err)
			}
			if phase != task.PhasePending {
				if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET phase=?,updated_at=? WHERE id=?`, phase, formatTime(now), resource.Metadata.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.RetireNode(ctx, "active-node", now.Add(time.Second)); !errors.Is(err, domain.ErrNodeActiveWork) {
				t.Fatalf("phase %s retirement=%v", phase, err)
			}
			if _, err := store.GetNode(ctx, "active-node", time.Minute); err != nil {
				t.Fatalf("blocked retirement hid node: %v", err)
			}
			if _, err := store.AuthenticateAgentCredential(ctx,
				mustParse(t, auth.AgentCredential, credential.Secret), "active-node", now.Add(2*time.Second)); err != nil {
				t.Fatalf("blocked retirement revoked credential: %v", err)
			}
		})
	}
}

func TestRetireNodeAllowsTerminalTaskHistory(t *testing.T) {
	for _, phase := range []task.Phase{task.PhaseCanceled, task.PhaseSucceeded, task.PhaseFailed} {
		phase := phase
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
			store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedTaskDependencies(t, store, "terminal-node", "terminal.check", now)
			resource := taskResource("terminal-task", "terminal-task-idem", "terminal-node", "terminal.check", now)
			if _, _, err := store.CreateTask(ctx, resource); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET phase=?,completed_at=?,updated_at=? WHERE id=?`,
				phase, formatTime(now), formatTime(now), resource.Metadata.ID); err != nil {
				t.Fatal(err)
			}
			if err := store.RetireNode(ctx, "terminal-node", now.Add(time.Second)); err != nil {
				t.Fatalf("terminal phase %s blocked retirement: %v", phase, err)
			}
			if _, err := store.GetTask(ctx, resource.Metadata.ID); err != nil {
				t.Fatalf("terminal task history lost: %v", err)
			}
		})
	}
}

func TestRetireNodeBlocksActiveOperationWithTerminalPlanningTask(t *testing.T) {
	for _, state := range []operation.State{
		operation.StateAwaitingConfirm,
		operation.StateFailed,
		operation.StateInterrupted,
	} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
			store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedOperationStateForRetirement(t, ctx, store, state, now)

			var phase task.Phase
			if err := store.db.QueryRowContext(ctx, `SELECT phase FROM tasks WHERE id='operation-task'`).Scan(&phase); err != nil {
				t.Fatal(err)
			}
			if !task.Terminal(phase) {
				t.Fatalf("planning task phase=%s is not terminal", phase)
			}
			if err := store.RetireNode(ctx, "operation-node", now.Add(time.Second)); !errors.Is(err, domain.ErrNodeActiveWork) {
				t.Fatalf("active operation state %s retirement=%v", state, err)
			}
			if _, err := store.GetNode(ctx, "operation-node", time.Minute); err != nil {
				t.Fatalf("active operation retirement hid node: %v", err)
			}
		})
	}
}

func TestRetireNodeAllowsTerminalOperationHistory(t *testing.T) {
	for _, state := range []operation.State{operation.StateSucceeded, operation.StateBlocked} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
			store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedOperationStateForRetirement(t, ctx, store, state, now)
			if err := store.RetireNode(ctx, "operation-node", now.Add(time.Second)); err != nil {
				t.Fatalf("terminal operation state %s blocked retirement: %v", state, err)
			}
			if _, err := store.GetOperationRun(ctx, "operation-run"); err != nil {
				t.Fatalf("terminal operation history lost: %v", err)
			}
		})
	}
}

func seedOperationStateForRetirement(t *testing.T, ctx context.Context, store *Store, state operation.State, now time.Time) {
	t.Helper()
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "operation-node", Hostname: "node", OS: "linux",
		OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "operation-node"}}
	spec := operationrun.Spec{OperationID: "operation.retirement", OperationVersion: "1.0.0", CapabilityDigest: "sha256:retirement",
		NodeID: "operation-node", Targets: targets, Parameters: json.RawMessage(`{}`)}
	planningTask := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask,
		Metadata: task.Metadata{ID: "operation-task", IdempotencyKey: "operation-task-idem", CreatedAt: now},
		Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion,
			CapabilityDigest: spec.CapabilityDigest, Targets: targets, Parameters: json.RawMessage(`{}`)},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: now}}
	run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun",
		Metadata: operationrun.Metadata{ID: "operation-run", IdempotencyKey: "operation-run-idem", CreatedAt: now},
		Spec:     spec, Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued",
			TaskID: planningTask.Metadata.ID, UpdatedAt: now}}
	if _, created, err := store.CreateOperationRun(ctx, run, planningTask); err != nil || !created {
		t.Fatalf("create operation run: created=%v err=%v", created, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET phase='succeeded',completed_at=?,updated_at=? WHERE id=?`,
		formatTime(now), formatTime(now), planningTask.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE operation_runs SET state=?,checkpoint='retirement-test',updated_at=? WHERE id=?`,
		state, formatTime(now), run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
}
