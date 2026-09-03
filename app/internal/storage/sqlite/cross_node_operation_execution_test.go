package sqlite

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func TestCrossNodeResultRejectsWrongAgentAndPersistsExactStageIdempotently(t *testing.T) {
	fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
	defer fixture.store.Close()
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
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: run.Spec.OperationID, RunID: run.Metadata.ID, Action: task.OperationActionCreateRestorePoint,
		PlanDigest: run.PlanDigest, ParticipantNodeIDs: participants, StageIndex: 0, Stage: plan.Steps[0],
		Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: fixture.nodeID}}, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	contractJSON, _ := json.Marshal(contract)
	taskTargets, _ := json.Marshal(contract.Targets)
	checkpoint := "stage_0_create_restore_point_queued"
	if _, err := fixture.store.db.Exec(`UPDATE operation_runs SET targets_json = ?, plan_json = ?, checkpoint = ? WHERE id = ?`, targetEnvelope, string(planJSON), checkpoint, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE operation_journal SET checkpoint = ? WHERE run_id = ? AND sequence = (SELECT MAX(sequence) FROM operation_journal WHERE run_id = ?)`, checkpoint, fixture.runID, fixture.runID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.db.Exec(`UPDATE tasks SET targets_json = ?, execution_contract_json = ?, execution_contract_digest = ? WHERE id = ?`, string(taskTargets), string(contractJSON), digest, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	point := testOperationRestorePoint(fixture.runID, fixture.base)
	result := &task.OperationExecutionResult{
		OperationID: run.Spec.OperationID, RunID: run.Metadata.ID, Action: task.OperationActionCreateRestorePoint,
		ParticipantNodeIDs: participants, StageID: contract.Stage.ID, StageIndex: 0, ExecutorNodeID: fixture.nodeID, RestorePoint: &point,
	}
	submission := task.ResultSubmission{ClaimID: fixture.claimID, Phase: task.PhaseSucceeded, OperationExecutionResult: result}
	if _, err := fixture.store.CompleteTask(fixture.ctx, otherNode, fixture.taskID, submission, fixture.reportedAt); !errors.Is(err, task.ErrNodeMismatch) {
		t.Fatalf("wrong Agent result error=%v", err)
	}
	for _, testCase := range []struct {
		name         string
		participants []string
	}{
		{name: "missing", participants: nil},
		{name: "reordered", participants: []string{otherNode, fixture.nodeID}},
		{name: "different", participants: []string{fixture.nodeID, "node-third"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			invalid := submission
			resultCopy := *result
			resultCopy.ParticipantNodeIDs = testCase.participants
			invalid.OperationExecutionResult = &resultCopy
			if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, invalid, fixture.reportedAt); err == nil {
				t.Fatal("invalid participant correlation was accepted")
			}
			if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 0 {
				t.Fatalf("invalid result persisted=%d", got)
			}
		})
	}
	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, submission, fixture.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseSucceeded {
		t.Fatalf("completed=%#v", completed)
	}
	stored, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Execution.Stages) != 1 || stored.Execution.Stages[0].StageID != "stage-local" || stored.Execution.Stages[0].ExecutorNodeID != fixture.nodeID || !reflect.DeepEqual(stored.Execution.Stages[0].RestorePoint, &point) {
		t.Fatalf("stage facts=%#v", stored.Execution)
	}
	journalCount := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, submission, fixture.reportedAt.Add(time.Minute)); err != nil {
		t.Fatalf("identical retry=%v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalCount {
		t.Fatalf("identical retry appended journal: before=%d after=%d", journalCount, got)
	}
	var databasePath string
	if err := fixture.store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.store, err = Open(fixture.ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.store.Close()
	restartedRun, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	restartedTask, err := fixture.store.GetTask(fixture.ctx, restartedRun.Status.TaskID)
	if err != nil || restartedTask.Spec.OperationExecution == nil || restartedTask.Spec.OperationExecution.StageIndex != 0 || restartedTask.Spec.OperationExecution.Stage.ExecutorNodeID != fixture.nodeID {
		t.Fatalf("restart task=%#v err=%v", restartedTask, err)
	}
	if !reflect.DeepEqual(restartedRun.Spec.ParticipantNodeIDs, participants) || len(restartedRun.Execution.Stages) != 1 || restartedRun.Execution.Stages[0].StageID != "stage-local" {
		t.Fatalf("restart run=%#v", restartedRun)
	}
	conflicting := submission
	resultCopy := *result
	pointCopy := point
	pointCopy.ID = "competing-restore-point"
	resultCopy.RestorePoint = &pointCopy
	conflicting.OperationExecutionResult = &resultCopy
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, conflicting, fixture.reportedAt.Add(2*time.Minute)); !errors.Is(err, task.ErrResultConflict) {
		t.Fatalf("competitor result error=%v", err)
	}
}
