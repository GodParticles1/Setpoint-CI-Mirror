package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type mismatchedExecutionAdapter struct{ operationID string }

func (adapter mismatchedExecutionAdapter) OperationID() string { return adapter.operationID }
func (adapter mismatchedExecutionAdapter) Resolve(context.Context, task.Resource) (ResolvedOperationExecution, error) {
	return ResolvedOperationExecution{OperationID: "operation.other", Definition: &actionTestDefinition{metadata: clickhouse.OperationMetadata()}, RestoreProvider: &actionTestRestore{}}, nil
}

func TestOperationExecutionResolverRejectsUnknownAndCrossCapabilityResolution(t *testing.T) {
	resolver, err := NewOperationExecutionResolver()
	if err != nil {
		t.Fatal(err)
	}
	resource := actionTask(t, clickhouse.OperationMetadata(), task.OperationActionCreateRestorePoint)
	if _, err := resolver.Resolve(context.Background(), resource); err == nil {
		t.Fatal("unknown operation capability resolved")
	}
	resolver, err = NewOperationExecutionResolver(mismatchedExecutionAdapter{operationID: clickhouse.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), resource); err == nil {
		t.Fatal("cross-capability adapter result was accepted")
	}
}

type clickHouseQueryStub struct{}

func (clickHouseQueryStub) Query(context.Context, clickhouse.QueryRequest) (string, error) {
	return "", errors.New("query not expected while resolving")
}

type clickHouseStagingStub struct{}

func (clickHouseStagingStub) Recreate(context.Context, clickhouse.Endpoint, string, string, string) error { return nil }
func (clickHouseStagingStub) Drop(context.Context, clickhouse.Endpoint, string, string) error             { return nil }

type clickHouseTransportStub struct{}

func (clickHouseTransportStub) Transfer(context.Context, clickhouse.NativeTransferRequest) (clickhouse.NativeTransferResult, error) {
	return clickhouse.NativeTransferResult{}, nil
}

type clickHouseVerifierStub struct{}

func (clickHouseVerifierStub) Fingerprint(context.Context, clickhouse.Endpoint, string, clickhouse.Table, *clickhouse.TimeRangeFilter) (clickhouse.DataFingerprint, error) {
	return clickhouse.DataFingerprint{}, nil
}

type clickHouseObjectsStub struct{}

func (clickHouseObjectsStub) Inspect(context.Context, clickhouse.Endpoint, string, string) (clickhouse.RestoreObjectSnapshot, error) {
	return clickhouse.RestoreObjectSnapshot{}, nil
}
func (clickHouseObjectsStub) Create(context.Context, clickhouse.Endpoint, string, string, clickhouse.Table) error {
	return nil
}
func (clickHouseObjectsStub) Drop(context.Context, clickhouse.Endpoint, string, string) error { return nil }

func TestClickHouseExecutionAndRestoreProvidersResolveThroughCapabilityAdapter(t *testing.T) {
	authority := &authorityStub{}
	adapter, err := NewClickHouseOperationExecutionAdapter(clickHouseQueryStub{}, clickHouseStagingStub{}, clickHouseTransportStub{}, clickHouseVerifierStub{}, clickHouseObjectsStub{}, authority)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewOperationExecutionResolver(adapter)
	if err != nil {
		t.Fatal(err)
	}
	resource := actionTask(t, clickhouse.OperationMetadata(), task.OperationActionCreateRestorePoint)
	resolved, err := resolver.Resolve(context.Background(), resource)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.OperationID != clickhouse.OperationID || resolved.Definition.Metadata().ID != clickhouse.OperationID || resolved.RestoreProvider.ID() != clickhouse.ExchangeRestoreProviderID {
		t.Fatalf("resolved=%#v", resolved)
	}
}

func TestClickHouseFullDefinitionExecutesThroughGenericAgentRunner(t *testing.T) {
	chunk := clickhouse.TransferChunk{RunID: "prepared", Strategy: clickhouse.StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "prepared", Sequence: 1}
	planArtifact, err := clickhouse.EncodeExchangeRestorePlan(clickhouse.ExchangeRestorePlan{Pair: clickhouse.PairParameters{Source: clickhouse.Endpoint{Host: "source"}, Target: clickhouse.Endpoint{Host: "target"}, Database: "db", Tables: []string{"events"}}, Items: []clickhouse.ExchangeRestoreItem{{Chunk: chunk, TargetTable: clickhouse.Table{Database: "db", Name: "events"}}}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := clickhouse.OperationMetadata()
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil {
		t.Fatal(err)
	}
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}, {Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}}
	contract, contractDigest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: metadata.ID,
		RunID: "run-1",
		Action: task.OperationActionVerify,
		PlanDigest: "sha256:plan",
		Targets: targets,
		Plan: operation.Plan{SchemaVersion: "clickhouse.operation_plan.v1", Execution: planArtifact},
		Apply: &operation.ApplyResult{Checkpoint: "clickhouse_apply", State: operation.Artifact{SchemaVersion: "clickhouse.apply_state.v1", Payload: json.RawMessage(`{"run_id":"run-1","committed":[]}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := clickhouse.LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1}
	authority := &authorityStub{ledgerFound: true, ledgerEntry: clickhouse.LedgerEntry{Key: key, State: clickhouse.LedgerCommitted}}
	adapter, err := NewClickHouseOperationExecutionAdapter(clickHouseQueryStub{}, clickHouseStagingStub{}, clickHouseTransportStub{}, clickHouseVerifierStub{}, clickHouseObjectsStub{}, authority)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewOperationExecutionResolver(adapter)
	if err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(clickhouse.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	runner, err := NewOperationExecutionRunner(registry, resolver, actionTestExecutor{}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	resource := task.Resource{Kind: task.KindOperationExecutionTask, Metadata: task.Metadata{ID: "task-1"}, Spec: task.Spec{NodeID: "node-1", OperationID: metadata.ID, OperationVersion: metadata.Version, CapabilityDigest: digest, Targets: targets, OperationExecution: &contract, ContractDigest: contractDigest}, Status: task.Status{Phase: task.PhaseRunning, ClaimID: "claim-1"}}
	result, err := runner.Execute(context.Background(), resource)
	if err != nil || result.Verification == nil || !result.Verification.Passed || authority.gets != 1 {
		t.Fatalf("result=%#v ledger_gets=%d err=%v", result, authority.gets, err)
	}
}

type mismatchedRestoreProvider struct{ actionTestRestore }

func (provider *mismatchedRestoreProvider) Create(ctx context.Context, request operation.RestorePointRequest) (operation.RestorePoint, error) {
	point, err := provider.actionTestRestore.Create(ctx, request)
	point.OperationID = "operation.other"
	return point, err
}

func TestOperationExecutionRunnerRejectsCrossCapabilityRestoreProvider(t *testing.T) {
	metadata := clickhouse.OperationMetadata()
	registry := operation.NewRegistry()
	definition := &actionTestDefinition{metadata: metadata}
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewStaticOperationExecutionAdapter(metadata.ID, definition, &mismatchedRestoreProvider{})
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
	result, runErr := runner.Execute(context.Background(), actionTask(t, metadata, task.OperationActionCreateRestorePoint))
	if runErr == nil || result.Error == nil || result.Error.Code != "restore_provider_mismatch" {
		t.Fatalf("result=%#v err=%v", result, runErr)
	}
}
