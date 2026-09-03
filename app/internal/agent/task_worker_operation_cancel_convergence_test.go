package agent

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func TestCompletedOperationActionIsNotRewrittenAsCanceledBeforeExecution(t *testing.T) {
	resource := operationExecutionCancelRaceTask()
	point := operation.RestorePoint{
		ID: "restore-cancel-race", ProviderID: "test.restore", OperationID: resource.Spec.OperationID,
		RunID: resource.Spec.OperationExecution.RunID, Status: operation.RestorePointVerified,
		Targets: resource.Spec.Targets, CreatedAt: time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC),
		Manifest: operation.Artifact{SchemaVersion: "test.restore.v1", Payload: []byte(`{"owned":true}`)},
	}
	submission := task.ResultSubmission{
		ClaimID: resource.Status.ClaimID, Phase: task.PhaseSucceeded,
		OperationExecutionResult: &task.OperationExecutionResult{
			OperationID: resource.Spec.OperationID, RunID: resource.Spec.OperationExecution.RunID,
			Action: task.OperationActionCreateRestorePoint, RestorePoint: &point,
		},
	}
	journal := mustTaskJournal(t)
	if err := journal.Save(taskJournalEntry{Version: 1, State: journalCompleted, Task: resource, Submission: &submission}); err != nil {
		t.Fatal(err)
	}
	remote := &operationCancelRaceRemote{current: resource}
	remote.current.Status.Phase = task.PhaseCancelRequested
	worker, err := NewTaskWorker(remote, resource.Spec.NodeID, "linux", testWorkerRegistry(t), &workerExecutor{}, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(remote.submissions) != 1 || remote.submissions[0].Phase != task.PhaseSucceeded ||
		remote.submissions[0].OperationExecutionResult == nil || remote.submissions[0].OperationExecutionResult.RestorePoint == nil {
		t.Fatalf("completed physical result was rewritten: %#v", remote.submissions)
	}
}

func TestCancelRequestedOperationTaskAcknowledgesExactlyOnce(t *testing.T) {
	resource := operationExecutionCancelRaceTask()
	resource.Status.Phase = task.PhaseCancelRequested
	remote := &operationCancelRaceRemote{current: resource}
	worker, err := NewTaskWorker(remote, resource.Spec.NodeID, "linux", testWorkerRegistry(t), &workerExecutor{}, mustTaskJournal(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(remote.submissions) != 1 || remote.submissions[0].Phase != task.PhaseCanceled ||
		remote.submissions[0].OperationExecutionResult == nil || remote.submissions[0].OperationExecutionResult.Error == nil ||
		remote.submissions[0].OperationExecutionResult.Error.Code != "task_canceled" {
		t.Fatalf("cancellation acknowledgements=%#v", remote.submissions)
	}
}

type operationCancelRaceRemote struct {
	current     task.Resource
	submissions []task.ResultSubmission
	terminal    bool
}

func (remote *operationCancelRaceRemote) ClaimTask(context.Context, string) (*task.Resource, error) {
	if remote.terminal {
		return nil, nil
	}
	copy := task.Clone(remote.current)
	return &copy, nil
}

func (remote *operationCancelRaceRemote) AcknowledgeTask(context.Context, string, string, string) (task.Resource, error) {
	return task.Clone(remote.current), nil
}

func (remote *operationCancelRaceRemote) SubmitTaskResult(_ context.Context, _, _ string, submission task.ResultSubmission) (task.Resource, error) {
	remote.submissions = append(remote.submissions, submission)
	remote.current.Status.Phase = submission.Phase
	remote.terminal = true
	return task.Clone(remote.current), nil
}

func operationExecutionCancelRaceTask() task.Resource {
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: "operation.test", RunID: "run-cancel-race", Action: task.OperationActionCreateRestorePoint,
		PlanDigest: "sha256:plan", Targets: targets,
		Plan: operation.Plan{SchemaVersion: "test.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: []byte(`{}`)}},
	})
	if err != nil {
		panic(err)
	}
	return task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindOperationExecutionTask,
		Metadata: task.Metadata{ID: "run-cancel-race:create_restore_point"},
		Spec: task.Spec{
			NodeID: "node-1", OperationID: "operation.test", OperationVersion: "1.0.0",
			CapabilityDigest: "sha256:cap", Targets: targets, OperationExecution: &contract, ContractDigest: digest,
		},
		Status: task.Status{Phase: task.PhaseRunning, ClaimID: "claim-cancel-race", Attempt: 1},
	}
}
