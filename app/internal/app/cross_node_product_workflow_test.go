package app

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

type crossNodeObserver struct{ nodes map[string]domain.Node }

func (observer *crossNodeObserver) GetNode(_ context.Context, id string, _ time.Duration) (domain.Node, error) {
	return observer.nodes[id], nil
}

func crossNodeWorkflowFixture(action task.OperationAction, phase task.Phase, stageIndex int, barrier operation.StageBarrier) (*ProductOperations, *productWorkflowRepo, *crossNodeObserver) {
	now := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	participants := []string{"node-a", "node-b"}
	stages := []operation.PlanStep{
		{ID: "stage-a", ExecutorNodeID: "node-a", Target: operation.Target{Kind: operation.TargetNode, NodeID: "node-a"}, Writes: true},
		{ID: "stage-b", ExecutorNodeID: "node-b", Target: operation.Target{Kind: operation.TargetNode, NodeID: "node-b"}, Writes: true, Barrier: barrier},
	}
	plan := operation.Plan{SchemaVersion: "test.cross-node.plan.v1", Steps: stages, Execution: operation.Artifact{SchemaVersion: "test.exec.v1", Payload: []byte(`{"ok":true}`)}}
	restoreA := &operation.RestorePoint{ID: "rp-a"}
	restoreB := &operation.RestorePoint{ID: "rp-b"}
	applyA := &operation.ApplyResult{Changed: true, Checkpoint: "applied-a", State: operation.Artifact{SchemaVersion: "state.v1", Payload: []byte(`{"node":"a"}`)}}
	applyB := &operation.ApplyResult{Changed: true, Checkpoint: "applied-b", State: operation.Artifact{SchemaVersion: "state.v1", Payload: []byte(`{"node":"b"}`)}}
	rollbackB := &operation.RollbackResult{Restored: true, Checkpoint: "rolled-back-b", State: operation.Artifact{SchemaVersion: "state.v1", Payload: []byte(`{"node":"b"}`)}}
	execution := &operationrun.ExecutionSnapshot{Stages: []operationrun.StageExecutionSnapshot{
		{StageIndex: 0, StageID: "stage-a", ExecutorNodeID: "node-a", RestorePoint: restoreA, Apply: applyA, Verification: &operation.Verification{Passed: true}, ApplyAt: now.Add(-4 * time.Minute)},
		{StageIndex: 1, StageID: "stage-b", ExecutorNodeID: "node-b", RestorePoint: restoreB, Apply: applyB, Rollback: rollbackB, ApplyAt: now.Add(-time.Minute)},
	}}
	state := map[task.OperationAction]operation.State{
		task.OperationActionCreateRestorePoint: operation.StateCreatingRestorePoint,
		task.OperationActionApply:              operation.StateRunning,
		task.OperationActionVerify:             operation.StateVerifying,
		task.OperationActionRollback:           operation.StateRollingBack,
		task.OperationActionVerifyRollback:     operation.StateRollingBack,
	}[action]
	run := operationrun.Resource{
		Metadata: operationrun.Metadata{ID: "run-cross"},
		Spec: operationrun.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "cap", NodeID: "node-a", ParticipantNodeIDs: participants,
			Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-a"}, {Kind: operation.TargetNode, NodeID: "node-b"}}, Parameters: []byte(`{}`)},
		Status: operationrun.Status{State: state, TaskID: "current", UpdatedAt: now}, Plan: &plan, PlanDigest: "plan", Impact: &operation.Impact{Risk: operation.RiskHigh}, Execution: execution,
	}
	stage := stages[stageIndex]
	contract := task.OperationExecutionContract{SchemaVersion: task.OperationExecutionContractVersion, OperationID: run.Spec.OperationID, RunID: run.Metadata.ID, Action: action,
		PlanDigest: run.PlanDigest, ParticipantNodeIDs: participants, StageIndex: stageIndex, Stage: stage, Targets: operationrun.StageTargets(run, stage), Plan: plan}
	current := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationExecutionTask, Metadata: task.Metadata{ID: "current"},
		Spec: task.Spec{NodeID: stage.ExecutorNodeID, OperationID: run.Spec.OperationID, OperationExecution: &contract}, Status: task.Status{Phase: phase}}
	run.Status.Checkpoint = stagedActionResultCheckpoint(contract, phase)
	repo := &productWorkflowRepo{run: run, tasks: map[string]task.Resource{"current": current}, journal: []operation.JournalEntry{{RunID: run.Metadata.ID, Sequence: 1, State: state, Checkpoint: run.Status.Checkpoint, Message: "terminal evidence", At: now}}}
	lease := &productWorkflowLease{lease: operation.LockLease{ID: "lease", OwnerID: run.Metadata.ID, AcquiredAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}, held: true}
	resolver, _ := NewProductExecutionResolver(ProductExecutionCapability{OperationID: run.Spec.OperationID, ApplyAvailable: true})
	observer := &crossNodeObserver{nodes: map[string]domain.Node{"node-a": {ID: "node-a", LastSeenAt: now}, "node-b": {ID: "node-b", LastSeenAt: now.Add(-2 * time.Minute)}}}
	clock := func() time.Time { return now }
	service := &ProductOperations{base: &OperationsService{nodes: observer, offlineAfter: time.Minute, now: clock}, runs: repo, lease: lease, execution: resolver, now: clock}
	return service, repo, observer
}

func TestCrossNodeStageNeverAdvancesWithoutTerminalEvidence(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionVerify, task.PhaseRunning, 0, "")
	repo.run.Status.Checkpoint = stageCheckpoint(repo.run, 0, "verify_queued")
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 0 {
		t.Fatal("stage N+1 queued before stage N terminal evidence")
	}

	service, repo, _ = crossNodeWorkflowFixture(task.OperationActionVerify, task.PhaseSucceeded, 0, "")
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	next := repo.tasks["run-cross:stage:1:create_restore_point"]
	if repo.continuations != 1 || next.Spec.NodeID != "node-b" || next.Spec.OperationExecution.StageIndex != 1 {
		t.Fatalf("next=%#v continuations=%d", next, repo.continuations)
	}
}

func TestCrossNodeReconnectBarrierRequiresLaterSameIdentityObservation(t *testing.T) {
	service, repo, observer := crossNodeWorkflowFixture(task.OperationActionApply, task.PhaseSucceeded, 1, operation.StageBarrierAgentReconnect)
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 0 || repo.run.Status.Checkpoint != "stage_1_reconnect_wait" {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 0 {
		t.Fatal("reconnect barrier advanced without a later same-identity observation")
	}
	node := observer.nodes["node-b"]
	node.LastSeenAt = repo.run.Execution.Stages[1].ApplyAt.Add(time.Second)
	observer.nodes["node-b"] = node
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if next := repo.tasks["run-cross:stage:1:verify"]; repo.continuations != 1 || next.Spec.NodeID != "node-b" {
		t.Fatalf("next=%#v continuations=%d", next, repo.continuations)
	}
}

func TestCrossNodeRollbackMovesToPreviousParticipantWithExactFacts(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionVerifyRollback, task.PhaseSucceeded, 1, "")
	repo.run.Execution.Stages[1].RollbackVerification = &operation.Verification{Passed: true}
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	next := repo.tasks["run-cross:stage:0:rollback"]
	if next.Spec.NodeID != "node-a" || next.Spec.OperationExecution.StageIndex != 0 || next.Spec.OperationExecution.RestorePoint.ID != "rp-a" || next.Spec.OperationExecution.Apply.Checkpoint != "applied-a" {
		t.Fatalf("participant-specific rollback=%#v", next)
	}
}

func TestCrossNodeCanceledVerificationQueuesRollbackAndNoNextApply(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionVerify, task.PhaseCanceled, 0, "")
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if _, exists := repo.tasks["run-cross:stage:1:create_restore_point"]; exists {
		t.Fatal("cancellation queued the next participant stage")
	}
	if _, exists := repo.tasks["run-cross:stage:0:rollback"]; !exists {
		t.Fatal("cancellation after Apply did not enter participant-specific rollback")
	}
}

func TestCrossNodeRestartKeepsExactCurrentStage(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhasePending, 1, "")
	before := repo.run.Status.TaskID
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 0 || repo.run.Status.TaskID != before || repo.tasks[before].Spec.OperationExecution.StageIndex != 1 {
		t.Fatalf("run=%#v task=%#v", repo.run.Status, repo.tasks[before])
	}
}

func TestCrossNodeInterruptedApplyNeverReplaysAfterResume(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionApply, task.PhaseFailed, 1, "")
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateInterrupted || repo.continuations != 0 {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	taskCount := len(repo.tasks)
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.tasks) != taskCount || repo.continuations != 0 {
		t.Fatal("resume replayed an uncertain multi-node Apply")
	}
}

func TestCrossNodeCancellationAfterPriorApplyNeverQueuesLaterStageApply(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseSucceeded, 1, "")
	repo.run.Status.Recovery = &operationrun.Recovery{Code: "cancellation_requested", SafeNext: "rollback_applied_stages"}
	repo.run.Execution.Stages[1].Apply = nil
	repo.run.Execution.Stages[1].ApplyAt = time.Time{}
	repo.run.Execution.Stages[1].Rollback = nil
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if _, exists := repo.tasks["run-cross:stage:1:apply"]; exists {
		t.Fatal("later-stage Apply queued after durable cancellation intent")
	}
	rollback, exists := repo.tasks["run-cross:stage:0:rollback"]
	if !exists || rollback.Spec.NodeID != "node-a" || rollback.Spec.OperationExecution.StageIndex != 0 {
		t.Fatalf("participant rollback=%#v exists=%v", rollback, exists)
	}
	if repo.run.Status.State == operation.StateCanceledBeforeApply {
		t.Fatal("run falsely declared canceled_before_apply after a durable Apply")
	}
	if service.lease.(*productWorkflowLease).releases != 0 {
		t.Fatal("cancellation released containment before participant rollback")
	}
}

func TestCrossNodeCancellationBoundaryRestartPreservesStageAndContainment(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseRunning, 1, "")
	repo.run.Status.Recovery = &operationrun.Recovery{Code: "cancellation_requested", SafeNext: "rollback_applied_stages"}
	repo.run.Status.Checkpoint = stageCheckpoint(repo.run, 1, "create_restore_point_queued")
	currentTask := repo.run.Status.TaskID
	beforeTasks := len(repo.tasks)
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateCreatingRestorePoint || repo.run.Status.TaskID != currentTask || len(repo.tasks) != beforeTasks {
		t.Fatalf("run=%#v tasks=%d", repo.run.Status, len(repo.tasks))
	}
	if service.lease.(*productWorkflowLease).releases != 0 {
		t.Fatal("restart released containment during cancellation boundary")
	}
}

func TestCrossNodeCancelAPIImmediatelyContinuesTerminalRestorePointToRollback(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseSucceeded, 1, "")
	service.base.runs = repo
	repo.run.Execution.Stages[1].Apply = nil
	repo.run.Execution.Stages[1].ApplyAt = time.Time{}
	repo.run.Execution.Stages[1].Rollback = nil
	canceled, err := service.CancelOperationRun(context.Background(), repo.run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := repo.tasks["run-cross:stage:1:apply"]; exists {
		t.Fatal("Cancel API queued later-stage Apply")
	}
	if _, exists := repo.tasks["run-cross:stage:0:rollback"]; !exists || canceled.Status.State != operation.StateRollingBack {
		t.Fatalf("canceled=%#v rollback_exists=%v", canceled.Status, exists)
	}
	if service.lease.(*productWorkflowLease).releases != 0 {
		t.Fatal("Cancel API released containment before rollback")
	}
}

func TestCrossNodeCancellationFailsClosedWhenPriorRollbackFactsAreIncomplete(t *testing.T) {
	service, repo, _ := crossNodeWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseSucceeded, 1, "")
	repo.run.Status.Recovery = &operationrun.Recovery{Code: operationrun.RecoveryCancellationRequested, SafeNext: "rollback_applied_stages"}
	repo.run.Execution.Stages[0].RestorePoint = nil
	repo.run.Execution.Stages[1].Apply = nil
	repo.run.Execution.Stages[1].ApplyAt = time.Time{}
	repo.run.Execution.Stages[1].Rollback = nil
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateInterrupted || repo.run.Status.Recovery == nil || repo.run.Status.Recovery.Code != operationrun.RecoveryCancellationReconcile {
		t.Fatalf("run=%#v", repo.run)
	}
	if _, exists := repo.tasks["run-cross:stage:1:apply"]; exists {
		t.Fatal("incomplete rollback facts queued Apply")
	}
	if service.lease.(*productWorkflowLease).releases != 0 {
		t.Fatal("reconciliation boundary released containment")
	}
}
