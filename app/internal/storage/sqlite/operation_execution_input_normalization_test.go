package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

func TestOperationExecutionTaskRunCorrelationUsesSecretRefListSemantics(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*operationrun.Resource, *task.Resource, *task.OperationExecutionContract)
		wantErr string
	}{
		{name: "empty run and nil task", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			run.Spec.SecretRefs = make([]operation.SecretRef, 0)
			resource.Spec.SecretRefs = nil
		}},
		{name: "nil run and empty task", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			run.Spec.SecretRefs = nil
			resource.Spec.SecretRefs = make([]operation.SecretRef, 0)
		}},
		{name: "same ordered nonempty refs", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			refs := []operation.SecretRef{{RequirementID: "database", Reference: "secret-ref-1"}}
			run.Spec.SecretRefs = append([]operation.SecretRef(nil), refs...)
			resource.Spec.SecretRefs = append([]operation.SecretRef(nil), refs...)
		}},
		{name: "different requirement", wantErr: "inputs do not match", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			run.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "source", Reference: "secret-ref-1"}}
			resource.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "target", Reference: "secret-ref-1"}}
		}},
		{name: "different reference", wantErr: "inputs do not match", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			run.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "database", Reference: "secret-ref-1"}}
			resource.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "database", Reference: "secret-ref-2"}}
		}},
		{name: "different ref count", wantErr: "inputs do not match", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			run.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "database", Reference: "secret-ref-1"}}
			resource.Spec.SecretRefs = nil
		}},
		{name: "different ref order", wantErr: "inputs do not match", mutate: func(run *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			run.Spec.SecretRefs = []operation.SecretRef{
				{RequirementID: "source", Reference: "secret-ref-1"},
				{RequirementID: "target", Reference: "secret-ref-2"},
			}
			resource.Spec.SecretRefs = []operation.SecretRef{
				{RequirementID: "target", Reference: "secret-ref-2"},
				{RequirementID: "source", Reference: "secret-ref-1"},
			}
		}},
		{name: "parameters remain byte bound", wantErr: "inputs do not match", mutate: func(_ *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			resource.Spec.Parameters = json.RawMessage(`{"changed":true}`)
		}},
		{name: "targets remain bound", wantErr: "targets do not match", mutate: func(_ *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			resource.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-other"}}
		}},
		{name: "capability digest remains bound", wantErr: "capability digest does not match", mutate: func(_ *operationrun.Resource, resource *task.Resource, _ *task.OperationExecutionContract) {
			resource.Spec.CapabilityDigest = "sha256:other"
		}},
		{name: "plan digest remains bound", wantErr: "plan digest does not match", mutate: func(_ *operationrun.Resource, _ *task.Resource, contract *task.OperationExecutionContract) {
			contract.PlanDigest = "sha256:other"
		}},
		{name: "plan remains bound", wantErr: "plan does not match", mutate: func(_ *operationrun.Resource, _ *task.Resource, contract *task.OperationExecutionContract) {
			contract.Plan.Summary = "different plan"
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			fixture := prepareOperationExecutionFixture(t, task.OperationActionCreateRestorePoint)
			defer fixture.store.Close()
			run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			resource, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
			if err != nil {
				t.Fatal(err)
			}
			contract := *resource.Spec.OperationExecution
			current.mutate(&run, &resource, &contract)
			err = validateOperationExecutionTaskRunCorrelation(resource, contract, run)
			if current.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), current.wantErr) {
				t.Fatalf("error=%v want substring %q", err, current.wantErr)
			}
		})
	}
}

func TestEmptySecretRefStorageRoundTripAcceptsRestoreAndContinuesOnce(t *testing.T) {
	fixture := prepareOperationExecutionFixtureWithSecretRefs(
		t, task.OperationActionCreateRestorePoint, make([]operation.SecretRef, 0),
	)
	defer fixture.store.Close()

	run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := fixture.store.GetTask(fixture.ctx, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Spec.SecretRefs == nil || len(run.Spec.SecretRefs) != 0 {
		t.Fatalf("persisted OperationRun refs=%#v, want non-nil empty", run.Spec.SecretRefs)
	}
	if resource.Spec.SecretRefs != nil {
		t.Fatalf("persisted bounded task refs=%#v, want nil", resource.Spec.SecretRefs)
	}

	journalBefore := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID)
	completed, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status.Phase != task.PhaseSucceeded {
		t.Fatalf("completed task=%#v", completed.Status)
	}
	afterResult, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if afterResult.Execution == nil || afterResult.Execution.RestorePoint == nil {
		t.Fatalf("restore point not persisted: %#v", afterResult.Execution)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 1 {
		t.Fatalf("restore result rows=%d", got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ? AND checkpoint = ?`, fixture.runID, operationActionResultCheckpoint(task.OperationActionCreateRestorePoint, task.PhaseSucceeded)); got != 1 {
		t.Fatalf("restore journal entries=%d", got)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM operation_journal WHERE run_id = ?`, fixture.runID); got != journalBefore+1 {
		t.Fatalf("journal entries=%d want=%d", got, journalBefore+1)
	}
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.nodeID, fixture.taskID, fixture.submission, fixture.reportedAt); err != nil {
		t.Fatalf("duplicate restore result: %v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM task_results WHERE task_id = ?`, fixture.taskID); got != 1 {
		t.Fatalf("duplicate restore result rows=%d", got)
	}

	next, journal, at := continuationApplyInput(t, fixture)
	beforeTasks := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`)
	continued, err := fixture.store.ContinueOperationRun(
		fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", next, journal, at,
	)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Status.State != operation.StateRunning || continued.Status.Checkpoint != "apply_queued" {
		t.Fatalf("continued status=%#v", continued.Status)
	}
	applyTask, err := fixture.store.GetTask(fixture.ctx, continued.Status.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if applyTask.Spec.OperationExecution == nil || applyTask.Spec.OperationExecution.Action != task.OperationActionApply {
		t.Fatalf("apply task=%#v", applyTask)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks+1 {
		t.Fatalf("task count=%d want=%d", got, beforeTasks+1)
	}
	if _, err := fixture.store.ContinueOperationRun(
		fixture.ctx, fixture.runID, fixture.taskID, operation.StateRunning, "apply_queued", next, journal, at,
	); err != nil {
		t.Fatalf("duplicate continuation: %v", err)
	}
	if got := countRows(t, fixture.store, `SELECT COUNT(*) FROM tasks`); got != beforeTasks+1 {
		t.Fatalf("duplicate continuation created task count=%d", got)
	}
}
