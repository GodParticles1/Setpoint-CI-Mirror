package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestCanceledRunKeepsLeaseUntilBoundTaskConverges(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
	repo.run.Status.State = operation.StateCanceledBeforeApply
	repo.run.Status.Checkpoint = "canceled_before_apply"
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if lease.releases != 0 {
		t.Fatalf("lease released while task was unresolved: %d", lease.releases)
	}
	if !lease.held {
		t.Fatal("locked target became reusable before task convergence")
	}
	taskResource := repo.tasks[repo.run.Status.TaskID]
	taskResource.Status.Phase = task.PhaseCanceled
	repo.tasks[repo.run.Status.TaskID] = taskResource
	if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	if lease.releases != 1 {
		t.Fatalf("lease was not released after task convergence: %d", lease.releases)
	}
}

func TestProductCancelSubmitCompositionConvergesAndReleasesExactlyOnce(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseRunning)
	service.base = &OperationsService{runs: repo, now: service.now}
	current := repo.tasks[repo.run.Status.TaskID]
	current.Status.ClaimID = "claim-cancel"
	repo.tasks[current.Metadata.ID] = current
	canceled, err := service.CancelOperationRun(context.Background(), repo.run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status.State != operation.StateCanceledBeforeApply || repo.tasks[current.Metadata.ID].Status.Phase != task.PhaseCancelRequested || !lease.held {
		t.Fatalf("cancel did not preserve unresolved ownership: run=%#v task=%#v held=%v", canceled.Status, repo.tasks[current.Metadata.ID].Status, lease.held)
	}
	nodes := &productCancellationNodes{repo: repo}
	product := &ProductService{Service: &Service{nodes: nodes, now: service.now}, continuation: service}
	contract := repo.tasks[current.Metadata.ID].Spec.OperationExecution
	submission := task.ResultSubmission{
		ClaimID: "claim-cancel", Phase: task.PhaseCanceled,
		OperationExecutionResult: &task.OperationExecutionResult{
			OperationID: contract.OperationID, RunID: contract.RunID, Action: contract.Action,
			ParticipantNodeIDs: append([]string(nil), contract.ParticipantNodeIDs...), StageID: contract.Stage.ID, StageIndex: contract.StageIndex, ExecutorNodeID: contract.Stage.ExecutorNodeID,
			Error: &task.Failure{Code: "task_canceled", Message: "cancellation acknowledged"},
		},
	}
	if _, err := product.SubmitTaskResult(context.Background(), current.Spec.NodeID, current.Metadata.ID, submission); err != nil {
		t.Fatal(err)
	}
	if repo.tasks[current.Metadata.ID].Status.Phase != task.PhaseCanceled || lease.held || lease.releases != 1 || repo.continuations != 0 {
		t.Fatalf("convergence task=%#v held=%v releases=%d continuations=%d", repo.tasks[current.Metadata.ID].Status, lease.held, lease.releases, repo.continuations)
	}
	if _, exists := repo.tasks[repo.run.Metadata.ID+":apply"]; exists {
		t.Fatal("canceled CreateRestorePoint queued Apply")
	}
	if _, err := product.SubmitTaskResult(context.Background(), current.Spec.NodeID, current.Metadata.ID, submission); err != nil {
		t.Fatalf("duplicate result: %v", err)
	}
	if lease.releases != 1 {
		t.Fatalf("duplicate result released lease again: %d", lease.releases)
	}
}

func TestCanceledRunRestartKeepsLeaseUntilTaskTerminal(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
	repo.run.Status.State = operation.StateCanceledBeforeApply
	repo.run.Status.Checkpoint = "canceled_before_apply"
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !lease.held || lease.releases != 0 {
		t.Fatalf("restart released unresolved lease: held=%v releases=%d", lease.held, lease.releases)
	}
	current := repo.tasks[repo.run.Status.TaskID]
	current.Status.Phase = task.PhaseCanceled
	repo.tasks[current.Metadata.ID] = current
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lease.held || lease.releases != 1 {
		t.Fatalf("restart did not release converged lease: held=%v releases=%d", lease.held, lease.releases)
	}
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lease.releases != 1 {
		t.Fatalf("duplicate restart released lease twice: %d", lease.releases)
	}
}

func TestCanceledPlanningTaskRestartNeverAcquiresLease(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
	repo.run.Status.State = operation.StateCanceledBeforeApply
	repo.run.Status.Checkpoint = "canceled_before_apply"
	current := repo.tasks[repo.run.Status.TaskID]
	current.Kind = task.KindOperationPlanningTask
	current.Spec.OperationExecution = nil
	current.Spec.ContractDigest = ""
	repo.tasks[current.Metadata.ID] = current
	lease.held = false
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lease.acquires != 0 || lease.releases != 0 || lease.held {
		t.Fatalf("planning cancellation changed lease: acquires=%d releases=%d held=%v", lease.acquires, lease.releases, lease.held)
	}
}

func TestCanceledLeaseReconciliationPrecedesCapabilityAvailability(t *testing.T) {
	t.Run("unresolved", func(t *testing.T) {
		service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
		repo.run.Status.State = operation.StateCanceledBeforeApply
		repo.run.Status.Checkpoint = "canceled_before_apply"
		service.execution, _ = NewProductExecutionResolver()
		lease.currentErr = operation.ErrLeaseAuthorityUnavailable
		if err := service.ResumeOperationRuns(context.Background()); err != nil {
			t.Fatal(err)
		}
		if lease.resumes != 1 || lease.acquires != 0 || !lease.held {
			t.Fatalf("unresolved containment resumes=%d acquires=%d held=%v", lease.resumes, lease.acquires, lease.held)
		}
	})
	t.Run("terminal", func(t *testing.T) {
		service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCanceled)
		repo.run.Status.State = operation.StateCanceledBeforeApply
		repo.run.Status.Checkpoint = "canceled_before_apply"
		service.execution, _ = NewProductExecutionResolver()
		if err := service.ResumeOperationRuns(context.Background()); err != nil {
			t.Fatal(err)
		}
		if lease.resumes != 1 || lease.releases != 1 || lease.acquires != 0 || lease.held {
			t.Fatalf("terminal convergence resumes=%d releases=%d acquires=%d held=%v", lease.resumes, lease.releases, lease.acquires, lease.held)
		}
	})
	t.Run("continue terminal", func(t *testing.T) {
		service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCanceled)
		repo.run.Status.State = operation.StateCanceledBeforeApply
		repo.run.Status.Checkpoint = "canceled_before_apply"
		service.execution, _ = NewProductExecutionResolver()
		if err := service.ContinueOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
			t.Fatal(err)
		}
		if lease.resumes != 1 || lease.releases != 1 || lease.acquires != 0 || lease.held {
			t.Fatalf("terminal continuation resumes=%d releases=%d acquires=%d held=%v", lease.resumes, lease.releases, lease.acquires, lease.held)
		}
	})
}

func TestCanceledRestorePointRestartAcquiresOnlyOnAuthoritativeAbsence(t *testing.T) {
	t.Run("absence", func(t *testing.T) {
		service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
		repo.run.Status.State = operation.StateCanceledBeforeApply
		repo.run.Status.Checkpoint = "canceled_before_apply"
		lease.held = false
		if err := service.ResumeOperationRuns(context.Background()); err != nil {
			t.Fatal(err)
		}
		if lease.resumes != 1 || lease.acquires != 1 || !lease.held {
			t.Fatalf("absence resumes=%d acquires=%d held=%v", lease.resumes, lease.acquires, lease.held)
		}
	})
	t.Run("authority failure", func(t *testing.T) {
		service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
		repo.run.Status.State = operation.StateCanceledBeforeApply
		repo.run.Status.Checkpoint = "canceled_before_apply"
		lease.held = false
		lease.resumeErr = operation.ErrLeaseAuthorityUnavailable
		if err := service.ResumeOperationRuns(context.Background()); !errors.Is(err, operation.ErrLeaseAuthorityUnavailable) {
			t.Fatalf("authority failure=%v", err)
		}
		if lease.acquires != 0 || lease.releases != 0 || lease.held {
			t.Fatalf("authority failure changed lease: acquires=%d releases=%d held=%v", lease.acquires, lease.releases, lease.held)
		}
	})
}

func TestCanceledRestorePointRestartRejectsUnfrozenTaskBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*task.Resource)
	}{
		{name: "digest", mutate: func(resource *task.Resource) { resource.Spec.ContractDigest = "tampered" }},
		{name: "action", mutate: func(resource *task.Resource) { resource.Spec.OperationExecution.Action = task.OperationActionApply }},
		{name: "targets", mutate: func(resource *task.Resource) {
			resource.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "other-node"}}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseCancelRequested)
			repo.run.Status.State = operation.StateCanceledBeforeApply
			repo.run.Status.Checkpoint = "canceled_before_apply"
			lease.held = false
			current := repo.tasks[repo.run.Status.TaskID]
			testCase.mutate(&current)
			repo.tasks[current.Metadata.ID] = current
			if err := service.ResumeOperationRuns(context.Background()); err == nil {
				t.Fatal("expected fail-closed binding rejection")
			}
			if lease.resumes != 0 || lease.acquires != 0 || lease.releases != 0 {
				t.Fatalf("invalid binding touched lease: resumes=%d acquires=%d releases=%d", lease.resumes, lease.acquires, lease.releases)
			}
		})
	}
}

func TestProductLateRestorePointSuccessAfterCancelNeverQueuesApply(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseRunning)
	service.base = &OperationsService{runs: repo, now: service.now}
	current := repo.tasks[repo.run.Status.TaskID]
	current.Status.ClaimID = "claim-late-success"
	repo.tasks[current.Metadata.ID] = current
	if _, err := service.CancelOperationRun(context.Background(), repo.run.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	product := &ProductService{Service: &Service{nodes: &productCancellationNodes{repo: repo}, now: service.now}, continuation: service}
	contract := repo.tasks[current.Metadata.ID].Spec.OperationExecution
	point := &operation.RestorePoint{ID: "restore-late", RunID: repo.run.Metadata.ID}
	submission := task.ResultSubmission{
		ClaimID: "claim-late-success", Phase: task.PhaseSucceeded,
		OperationExecutionResult: &task.OperationExecutionResult{
			OperationID: contract.OperationID, RunID: contract.RunID, Action: contract.Action, RestorePoint: point,
			ParticipantNodeIDs: append([]string(nil), contract.ParticipantNodeIDs...), StageID: contract.Stage.ID, StageIndex: contract.StageIndex, ExecutorNodeID: contract.Stage.ExecutorNodeID,
		},
	}
	if _, err := product.SubmitTaskResult(context.Background(), current.Spec.NodeID, current.Metadata.ID, submission); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateCanceledBeforeApply || repo.run.Execution == nil || repo.run.Execution.RestorePoint == nil {
		t.Fatalf("late RestorePoint was not retained: %#v", repo.run)
	}
	if repo.tasks[current.Metadata.ID].Status.Phase != task.PhaseSucceeded || lease.held || lease.releases != 1 {
		t.Fatalf("late success did not converge: task=%#v held=%v releases=%d", repo.tasks[current.Metadata.ID].Status, lease.held, lease.releases)
	}
	if repo.continuations != 0 {
		t.Fatalf("canceled late success queued continuations=%d", repo.continuations)
	}
	if _, exists := repo.tasks[repo.run.Metadata.ID+":apply"]; exists {
		t.Fatal("late RestorePoint success queued Apply")
	}
}

type productCancellationNodes struct {
	NodeRepository
	repo *productWorkflowRepo
}

func (nodes *productCancellationNodes) GetTask(_ context.Context, id string) (task.Resource, error) {
	return nodes.repo.GetTask(context.Background(), id)
}

func (nodes *productCancellationNodes) CompleteTask(_ context.Context, agentID, taskID string, submission task.ResultSubmission, at time.Time) (task.Resource, error) {
	resource := nodes.repo.tasks[taskID]
	if resource.Spec.NodeID != agentID || resource.Status.ClaimID != submission.ClaimID {
		return task.Resource{}, task.ErrClaimMismatch
	}
	if task.Terminal(resource.Status.Phase) {
		return resource, nil
	}
	resource.Status.Phase = submission.Phase
	resource.Status.CompletedAt = &at
	resource.Status.UpdatedAt = at
	resource.OperationExecutionResult = submission.OperationExecutionResult
	nodes.repo.tasks[taskID] = resource
	if submission.OperationExecutionResult != nil && submission.OperationExecutionResult.RestorePoint != nil {
		if nodes.repo.run.Execution == nil {
			nodes.repo.run.Execution = &operationrun.ExecutionSnapshot{}
		}
		nodes.repo.run.Execution.RestorePoint = submission.OperationExecutionResult.RestorePoint
	}
	return resource, nil
}

var _ NodeRepository = (*productCancellationNodes)(nil)
var _ operationContinuation = (*ProductOperations)(nil)
