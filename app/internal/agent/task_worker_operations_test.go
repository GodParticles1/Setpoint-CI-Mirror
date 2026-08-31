package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

func TestTaskWorkerExecutesOperationPlanningLocally(t *testing.T) {
	execution := &workerExecutor{}
	definition := &workerPlanningDefinition{}
	registry := operation.NewRegistry()
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	resource := planningWorkerTask(t, definition, nil)
	remote := &planningWorkerRemote{resource: resource}
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewTaskWorkerWithOperations(remote, "agent-1", "linux", testWorkerRegistry(t), registry, execution, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if execution.callCount() != 1 || len(remote.submissions) != 1 {
		t.Fatalf("executor=%d submissions=%d", execution.callCount(), len(remote.submissions))
	}
	result := remote.submissions[0].OperationResult
	if remote.submissions[0].Result != nil || result == nil || result.State != operation.StateAwaitingConfirm || result.PlanDigest == "" || result.Plan == nil || result.Impact == nil {
		t.Fatalf("submission=%#v", remote.submissions[0])
	}
}

func TestTaskWorkerBlocksSecretRefWithoutRuntimeDelivery(t *testing.T) {
	execution := &workerExecutor{}
	definition := &workerPlanningDefinition{}
	registry := operation.NewRegistry()
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	resource := planningWorkerTask(t, definition, []operation.SecretRef{{RequirementID: "credential", Reference: "ref-1"}})
	remote := &planningWorkerRemote{resource: resource}
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewTaskWorkerWithOperations(remote, "agent-1", "linux", testWorkerRegistry(t), registry, execution, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := remote.submissions[0].OperationResult
	if execution.callCount() != 0 || result == nil || result.State != operation.StateBlocked || result.Block == nil || result.Block.Code != "secret_delivery_unavailable" {
		t.Fatalf("executor=%d result=%#v", execution.callCount(), result)
	}
}

type countingOperationRunner struct{ calls int }

func (runner *countingOperationRunner) Execute(context.Context, task.Resource) (task.OperationExecutionResult, error) {
	runner.calls++
	return task.OperationExecutionResult{}, nil
}

func TestTaskWorkerRestartResubmitsCompletedOperationActionWithoutReplay(t *testing.T) {
	metadata := (&actionTestDefinition{metadata: clickhouse.OperationMetadata()}).Metadata()
	resource := actionTask(t, metadata, task.OperationActionCreateRestorePoint)
	point := operation.RestorePoint{ID: "rp-1", ProviderID: "test.restore", OperationID: metadata.ID, RunID: "run-1", Status: operation.RestorePointVerified, Targets: append([]operation.Target(nil), resource.Spec.Targets...), CreatedAt: time.Now().UTC(), Manifest: operation.Artifact{SchemaVersion: "test.restore.v1", Payload: json.RawMessage(`{}`)}}
	submission := task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: task.PhaseSucceeded, OperationExecutionResult: &task.OperationExecutionResult{OperationID: metadata.ID, RunID: "run-1", Action: task.OperationActionCreateRestorePoint, RestorePoint: &point}}
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Save(taskJournalEntry{Version: 1, State: journalCompleted, Task: resource, Submission: &submission}); err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(&actionTestDefinition{metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	remote := &planningWorkerRemote{resource: resource}
	execution := &countingOperationRunner{}
	worker, err := NewTaskWorkerWithControlledOperations(remote, "agent-1", "linux", testWorkerRegistry(t), registry, execution, &workerExecutor{}, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if execution.calls != 0 || len(remote.submissions) != 1 || remote.submissions[0].OperationExecutionResult == nil {
		t.Fatalf("execution_calls=%d submissions=%#v", execution.calls, remote.submissions)
	}
}

type workerPlanningDefinition struct{}

func (*workerPlanningDefinition) Metadata() operation.Metadata {
	return operation.Metadata{ID: "operation.test.plan", Category: "test", Name: "Test plan", Version: "1.0.0", Risk: operation.RiskLow, Impact: "none", SupportedSystems: []string{"linux"}, SecretRequirements: []operation.SecretRequirement{{ID: "credential"}}}
}

func (*workerPlanningDefinition) Discover(ctx context.Context, input operation.DiscoverInput) (operation.Discovery, error) {
	_, err := input.Runtime.Executor.Execute(ctx, executor.Command{Name: "true"})
	return operation.Discovery{Applicable: true, Summary: "discovered", Targets: input.Runtime.Targets, Snapshot: operation.Artifact{SchemaVersion: "test.discovery.v1", Payload: json.RawMessage(`{}`)}}, err
}

func (*workerPlanningDefinition) Precheck(context.Context, operation.PrecheckInput) (operation.Precheck, error) {
	return operation.Precheck{Passed: true, Summary: "passed", Snapshot: operation.Artifact{SchemaVersion: "test.precheck.v1", Payload: json.RawMessage(`{}`)}}, nil
}

func (*workerPlanningDefinition) Plan(context.Context, operation.PlanInput) (operation.Plan, error) {
	return operation.Plan{SchemaVersion: "test.plan.v1", Summary: "plan", Steps: []operation.PlanStep{}, Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}, nil
}

func (*workerPlanningDefinition) Impact(context.Context, operation.ImpactInput) (operation.Impact, error) {
	return operation.Impact{Summary: "impact", Risk: operation.RiskLow, Changes: []operation.Change{}}, nil
}

func planningWorkerTask(t *testing.T, definition operation.PlanningDefinition, refs []operation.SecretRef) task.Resource {
	t.Helper()
	metadata := definition.Metadata()
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask,
		Metadata: task.Metadata{ID: "operation-task-1", IdempotencyKey: "operation-idem-1", CreatedAt: now},
		Spec: task.Spec{NodeID: "agent-1", OperationID: metadata.ID, OperationVersion: metadata.Version, CapabilityDigest: digest,
			Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "agent-1"}}, Parameters: json.RawMessage(`{}`), SecretRefs: refs},
		Status: task.Status{Phase: task.PhaseClaimed, ClaimID: "claim-op-1", Attempt: 1, UpdatedAt: now}}
}

type planningWorkerRemote struct {
	resource    task.Resource
	claimed     bool
	submissions []task.ResultSubmission
}

func (remote *planningWorkerRemote) ClaimTask(context.Context, string) (*task.Resource, error) {
	if remote.claimed {
		return nil, nil
	}
	remote.claimed = true
	copy := task.Clone(remote.resource)
	return &copy, nil
}

func (remote *planningWorkerRemote) AcknowledgeTask(context.Context, string, string, string) (task.Resource, error) {
	copy := task.Clone(remote.resource)
	copy.Status.Phase = task.PhaseRunning
	return copy, nil
}

func (remote *planningWorkerRemote) SubmitTaskResult(_ context.Context, _, _ string, submission task.ResultSubmission) (task.Resource, error) {
	remote.submissions = append(remote.submissions, submission)
	copy := task.Clone(remote.resource)
	copy.Status.Phase = submission.Phase
	return copy, nil
}
