package app

import (
	"context"
	"testing"

	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func TestProductContinuationRollbackSuccessQueuesVerifyRollbackThenRollsBack(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionRollback, task.PhaseSucceeded)
	beforeTasks := len(repo.tasks)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 || repo.run.Status.State != operation.StateRollingBack || repo.run.Status.TaskID != "run-1:verify_rollback" {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	if len(repo.tasks) != beforeTasks+1 {
		t.Fatalf("next task count=%d want=%d", len(repo.tasks), beforeTasks+1)
	}
	next := repo.tasks["run-1:verify_rollback"]
	if next.Spec.OperationExecution == nil || next.Spec.OperationExecution.Action != task.OperationActionVerifyRollback || next.Spec.OperationExecution.RestorePoint == nil || next.Spec.OperationExecution.Rollback == nil {
		t.Fatalf("verify rollback task=%#v", next)
	}
	if lease.releases != 0 {
		t.Fatalf("lease released before rollback verification: %d", lease.releases)
	}

	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 || len(repo.tasks) != beforeTasks+1 {
		t.Fatalf("identical continuation duplicated task: continuations=%d tasks=%d", repo.continuations, len(repo.tasks))
	}

	next.Status.Phase = task.PhaseSucceeded
	repo.tasks[next.Metadata.ID] = next
	repo.run.Status.Checkpoint = "action_verify_rollback_succeeded"
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateRolledBack || repo.run.Status.Checkpoint != "rollback_verified" {
		t.Fatalf("terminal run=%#v", repo.run.Status)
	}
	if lease.releases != 1 {
		t.Fatalf("lease releases=%d", lease.releases)
	}
	if len(repo.tasks) != beforeTasks+1 {
		t.Fatalf("terminal continuation created another task: %d", len(repo.tasks))
	}
}

func TestProductContinuationRollbackFailureRoutesByTypedMutationState(t *testing.T) {
	tests := []struct {
		name string
		state operation.MutationState
		code string
	}{
		{name: "not-started", state: operation.MutationNotStarted, code: "rollback_not_started"},
		{name: "changed", state: operation.MutationChanged, code: "rollback_changed_but_failed"},
		{name: "may-have-changed", state: operation.MutationMayHaveChanged, code: "rollback_outcome_uncertain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, repo, lease := productWorkflowFixture(task.OperationActionRollback, task.PhaseFailed)
			repo.run.Execution.Rollback = &operation.RollbackResult{Restored: false, MutationState: tc.state, Checkpoint: "partial"}
			beforeTasks := len(repo.tasks)
			if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
				t.Fatal(err)
			}
			if repo.continuations != 0 || repo.run.Status.State != operation.StateRollbackFailed || repo.run.Status.Checkpoint != "rollback_failed" {
				t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
			}
			if repo.run.Status.Recovery == nil || repo.run.Status.Recovery.Code != tc.code || !repo.run.Status.Recovery.ManualReview {
				t.Fatalf("recovery=%#v", repo.run.Status.Recovery)
			}
			if len(repo.tasks) != beforeTasks || lease.releases != 1 {
				t.Fatalf("tasks=%d/%d releases=%d", len(repo.tasks), beforeTasks, lease.releases)
			}
		})
	}
}

func TestProductContinuationVerifyRollbackFailureTerminates(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionVerifyRollback, task.PhaseFailed)
	beforeTasks := len(repo.tasks)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateRollbackFailed || repo.run.Status.Checkpoint != "rollback_verification_failed" {
		t.Fatalf("run=%#v", repo.run.Status)
	}
	if repo.run.Status.Recovery == nil || repo.run.Status.Recovery.Code != "rollback_verification_failed" || len(repo.tasks) != beforeTasks || lease.releases != 1 {
		t.Fatalf("recovery=%#v tasks=%d/%d releases=%d", repo.run.Status.Recovery, len(repo.tasks), beforeTasks, lease.releases)
	}
}
