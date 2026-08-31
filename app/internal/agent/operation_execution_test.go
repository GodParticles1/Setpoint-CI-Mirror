package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type actionTestExecutor struct{}

func (actionTestExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	return executor.Result{}, nil
}

type actionTestDefinition struct{ metadata operation.Metadata }

func (definition *actionTestDefinition) Metadata() operation.Metadata { return definition.metadata }
func (*actionTestDefinition) Discover(context.Context, operation.DiscoverInput) (operation.Discovery, error) {
	return operation.Discovery{}, errors.New("must not be called")
}
func (*actionTestDefinition) Precheck(context.Context, operation.PrecheckInput) (operation.Precheck, error) {
	return operation.Precheck{}, errors.New("must not be called")
}
func (*actionTestDefinition) Plan(context.Context, operation.PlanInput) (operation.Plan, error) {
	return operation.Plan{}, errors.New("must not be called")
}
func (*actionTestDefinition) Impact(context.Context, operation.ImpactInput) (operation.Impact, error) {
	return operation.Impact{}, errors.New("must not be called")
}
func (*actionTestDefinition) Apply(context.Context, operation.ApplyInput) (operation.ApplyResult, error) {
	return operation.ApplyResult{}, errors.New("bounded test definition does not apply")
}
func (*actionTestDefinition) Verify(context.Context, operation.VerifyInput) (operation.Verification, error) {
	return operation.Verification{Passed: true, Summary: "verified"}, nil
}
func (*actionTestDefinition) Rollback(context.Context, operation.RollbackInput) (operation.RollbackResult, error) {
	return operation.RollbackResult{}, errors.New("bounded test definition does not rollback")
}
func (*actionTestDefinition) VerifyRollback(context.Context, operation.VerifyRollbackInput) (operation.Verification, error) {
	return operation.Verification{Passed: true, Summary: "rollback verified"}, nil
}

type actionTestRestore struct{ creates int }

func (*actionTestRestore) ID() string { return "test.restore" }
func (provider *actionTestRestore) Create(_ context.Context, request operation.RestorePointRequest) (operation.RestorePoint, error) {
	provider.creates++
	return operation.RestorePoint{ID: "rp-1", ProviderID: provider.ID(), OperationID: request.OperationID, RunID: request.RunID, Status: operation.RestorePointVerified, Targets: request.Targets, CreatedAt: time.Now().UTC(), Manifest: operation.Artifact{SchemaVersion: "test.restore.v1", Payload: json.RawMessage(`{}`)}}, nil
}
func (*actionTestRestore) Verify(context.Context, operation.RestorePoint) (operation.Verification, error) {
	return operation.Verification{Passed: true}, nil
}
func (*actionTestRestore) Restore(context.Context, operation.RestorePoint, operation.ApplyResult) (operation.RollbackResult, error) {
	return operation.RollbackResult{}, errors.New("must not be called")
}
func (*actionTestRestore) VerifyRestored(context.Context, operation.RestorePoint, operation.RollbackResult) (operation.Verification, error) {
	return operation.Verification{Passed: true}, nil
}

func newActionTestRunner(t *testing.T) (*operationExecutionRunner, operation.Metadata, *actionTestRestore) {
	t.Helper()
	metadata := clickhouse.OperationMetadata()
	registry := operation.NewRegistry()
	if err := registry.Register(&actionTestDefinition{metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	restore := &actionTestRestore{}
	adapter, err := NewStaticOperationExecutionAdapter(metadata.ID, &actionTestDefinition{metadata: metadata}, restore)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewOperationExecutionResolver(adapter)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewOperationExecutionRunner(registry, resolver, actionTestExecutor{}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	return runner.(*operationExecutionRunner), metadata, restore
}

func actionTask(t *testing.T, metadata operation.Metadata, action task.OperationAction) task.Resource {
	t.Helper()
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil {
		t.Fatal(err)
	}
	contract := task.OperationExecutionContract{
		OperationID: metadata.ID, RunID: "run-1", Action: action, PlanDigest: "sha256:plan",
		Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}},
		Plan:    operation.Plan{SchemaVersion: "test.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.exec.v1", Payload: json.RawMessage(`{}`)}},
	}
	if action == task.OperationActionApply || action == task.OperationActionRollback || action == task.OperationActionVerifyRollback {
		contract.RestorePoint = &operation.RestorePoint{ID: "rp-1", RunID: "run-1"}
	}
	if action == task.OperationActionApply {
		contract.Impact = &operation.Impact{Summary: "bounded apply", Risk: operation.RiskHigh}
	}
	if action == task.OperationActionVerify || action == task.OperationActionRollback {
		contract.Apply = &operation.ApplyResult{}
	}
	if action == task.OperationActionVerifyRollback {
		contract.Rollback = &operation.RollbackResult{}
	}
	contract, contractDigest, err := task.NewOperationExecutionContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	return task.Resource{
		Kind:     task.KindOperationExecutionTask,
		Metadata: task.Metadata{ID: "task-1"},
		Spec:     task.Spec{NodeID: "node-1", OperationID: metadata.ID, OperationVersion: metadata.Version, CapabilityDigest: digest, Targets: append([]operation.Target(nil), contract.Targets...), Parameters: json.RawMessage(`{}`), OperationExecution: &contract, ContractDigest: contractDigest},
		Status:   task.Status{Phase: task.PhaseRunning, ClaimID: "claim-1"},
	}
}

func TestOperationExecutionRunnerIsBoundedAndDoesNotOwnCoordinatorLifecycle(t *testing.T) {
	runner, metadata, restore := newActionTestRunner(t)
	resource := actionTask(t, metadata, task.OperationActionCreateRestorePoint)
	result, err := runner.Execute(context.Background(), resource)
	if err != nil || result.Action != task.OperationActionCreateRestorePoint || result.RestorePoint == nil || restore.creates != 1 {
		t.Fatalf("result=%#v creates=%d err=%v", result, restore.creates, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"lifecycle", "journal", "lease", "ledger", "runtime_snapshot"} {
		if containsJSONKey(encoded, forbidden) {
			t.Fatalf("Agent result owns forbidden %q state: %s", forbidden, encoded)
		}
	}
}

func TestOperationExecutionRunnerDestructiveActionsRequireBothServerAuthorityAdapters(t *testing.T) {
	runner, metadata, _ := newActionTestRunner(t)
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		result, err := runner.Execute(context.Background(), actionTask(t, metadata, action))
		if err == nil || result.Error == nil || result.Error.Code != "operation_authority_unavailable" {
			t.Fatalf("action=%s result=%#v err=%v", action, result, err)
		}
	}
}

func TestOperationExecutionRunnerRejectsSecretRefsAndTargetMismatch(t *testing.T) {
	runner, metadata, _ := newActionTestRunner(t)
	secret := actionTask(t, metadata, task.OperationActionCreateRestorePoint)
	secret.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "clickhouse_source_credential", Reference: "ref-1"}}
	result, err := runner.Execute(context.Background(), secret)
	if err == nil || result.Error == nil || result.Error.Code != "secret_delivery_unavailable" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	mismatch := actionTask(t, metadata, task.OperationActionCreateRestorePoint)
	mismatch.Spec.Targets[0].NodeID = "other-node"
	result, err = runner.Execute(context.Background(), mismatch)
	if err == nil || result.Error == nil || result.Error.Code != "operation_target_mismatch" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func containsJSONKey(raw []byte, key string) bool {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	_, ok := value[key]
	return ok
}
