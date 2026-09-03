package sqlite

import (
	"encoding/json"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestCrossNodeCancelLaterRestorePointKeepsTruthAndContainmentAcrossRestart(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	otherNode := "node-other"
	if _, err := fixture.store.RegisterNode(fixture.ctx, domain.Registration{AgentID: otherNode, Hostname: otherNode, OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: fixture.base}); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	participants := []string{fixture.nodeID, otherNode}
	plan := *run.Plan
	plan.Steps = []operation.PlanStep{
		{ID: "stage-local", ExecutorNodeID: fixture.nodeID, Target: operation.Target{Kind: operation.TargetNode, NodeID: fixture.nodeID}, Writes: true},
		{ID: "stage-other", ExecutorNodeID: otherNode, Target: operation.Target{Kind: operation.TargetNode, NodeID: otherNode}, Writes: true},
	}
	run.Spec.ParticipantNodeIDs = participants
	run.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: fixture.nodeID}, {Kind: operation.TargetNode, NodeID: otherNode}}
	targetEnvelope, _, _, err := encodeOperationRunSpec(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	planJSON, _ := json.Marshal(plan)
	stageZeroRestore := testOperationRestorePoint(fixture.runID, fixture.base)
	stageZeroApply := operation.ApplyResult{Changed: true, Checkpoint: "stage-local-applied", State: operation.Artifact{SchemaVersion: "state.v1", Payload: json.RawMessage(`{"stage":0}`)}}
	execution := operationrun.ExecutionSnapshot{Stages: []operationrun.StageExecutionSnapshot{{
		StageIndex: 0, StageID: "stage-local", ExecutorNodeID: fixture.nodeID,
		RestorePoint: &stageZeroRestore, Apply: &stageZeroApply, Verification: &operation.Verification{Passed: true},
		RestorePointAt: fixture.base.Add(time.Second), ApplyAt: fixture.base.Add(2 * time.Second), VerificationAt: fixture.base.Add(3 * time.Second),
	}}}
	executionJSON, _ := json.Marshal(execution)
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: run.Spec.OperationID, RunID: run.Metadata.ID, Action: task.OperationActionCreateRestorePoint,
		PlanDigest: run.PlanDigest, ParticipantNodeIDs: participants, StageIndex: 1, Stage: plan.Steps[1],
		Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: otherNode}}, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	contractJSON, _ := json.Marshal(contract)
	taskTargets, _ := json.Marshal(contract.Targets)
	checkpoint := "stage_1_create_restore_point_queued"
	if _, err := fixture.store.db.Exec(`UPDATE operation_runs SET targets_json = ?, plan_json = ?, execution_json = ?, checkpoint = ? WHERE id = ?`, targetEnvelope, string(planJSON), string(executionJSON), checkpoint, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE operation_journal SET checkpoint = ? WHERE run_id = ? AND sequence = (SELECT MAX(sequence) FROM operation_journal WHERE run_id = ?)`, checkpoint, fixture.runID, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE tasks SET node_id = ?, targets_json = ?, execution_contract_json = ?, execution_contract_digest = ? WHERE id = ?`, otherNode, string(taskTargets), string(contractJSON), digest, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.store.Acquire(fixture.ctx, operation.LockRequest{OwnerID: fixture.runID, TTL: time.Hour, Resources: []operation.LockResource{{Key: "node||node-execution||"}, {Key: "node||node-other||"}}})
	if err != nil {
		t.Fatal(err)
	}

	canceledAt := fixture.reportedAt.Add(time.Second)
	canceled, err := fixture.store.CancelOperationRun(fixture.ctx, fixture.runID, canceledAt)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status.State == operation.StateCanceledBeforeApply || canceled.Status.State != operation.StateCreatingRestorePoint || canceled.Status.Checkpoint != checkpoint {
		t.Fatalf("false cancellation state=%#v", canceled.Status)
	}
	if canceled.Status.Recovery == nil || canceled.Status.Recovery.Code != "cancellation_requested" {
		t.Fatalf("recovery=%#v", canceled.Status.Recovery)
	}
	journal, err := fixture.store.List(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	tail := journal[len(journal)-1]
	if tail.State != operation.StateCreatingRestorePoint || tail.Checkpoint != checkpoint {
		t.Fatalf("false cancellation journal tail=%#v", tail)
	}
	current, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil || current.Status.Phase != task.PhaseCancelRequested || current.Spec.OperationExecution.StageIndex != 1 {
		t.Fatalf("current=%#v err=%v", current, err)
	}
	if persisted, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, fixture.runID); err != nil || !found || persisted.ID != lease.ID {
		t.Fatalf("lease=%#v found=%v err=%v", persisted, found, err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks WHERE id = ?`, fixture.runID+":stage:1:apply"); got != 0 {
		t.Fatalf("stage 1 Apply tasks=%d", got)
	}

	var databasePath string
	if err := fixture.store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.store, err = open(fixture.ctx, databasePath, func() time.Time { return canceledAt })
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.store.Close()
	restarted, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	restartedTask, err := fixture.store.GetTask(fixture.ctx, restarted.Status.TaskID)
	if err != nil || restarted.Status.State != operation.StateCreatingRestorePoint || restarted.Status.Checkpoint != checkpoint || restarted.Status.Recovery == nil || restarted.Status.Recovery.Code != "cancellation_requested" || restartedTask.Spec.OperationExecution.StageIndex != 1 {
		t.Fatalf("restarted run=%#v task=%#v err=%v", restarted, restartedTask, err)
	}
	if _, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, fixture.runID); err != nil || !found {
		t.Fatalf("restart lease found=%v err=%v", found, err)
	}
}
