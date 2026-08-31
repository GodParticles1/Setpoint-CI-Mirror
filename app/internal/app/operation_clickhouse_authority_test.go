package app

import (
	"context"
	"encoding/json"
	"testing"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/operation/sysctlrepair"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type clickHouseAuthorityRepository struct {
	NodeRepository
	resource task.Resource
	ledgerPuts int
	restorePuts int
	restore clickhouse.RestoreRecord
}

func (repository *clickHouseAuthorityRepository) GetTask(context.Context, string) (task.Resource, error) {
	return task.Clone(repository.resource), nil
}
func (repository *clickHouseAuthorityRepository) Put(context.Context, clickhouse.LedgerEntry) error {
	repository.ledgerPuts++
	return nil
}
func (*clickHouseAuthorityRepository) Get(context.Context, clickhouse.LedgerKey) (clickhouse.LedgerEntry, bool, error) {
	return clickhouse.LedgerEntry{}, false, nil
}
func (*clickHouseAuthorityRepository) ListRun(context.Context, string) ([]clickhouse.LedgerEntry, error) {
	return nil, nil
}
func (repository *clickHouseAuthorityRepository) PutRestore(_ context.Context, record clickhouse.RestoreRecord) error {
	repository.restorePuts++
	repository.restore = record
	return nil
}
func (repository *clickHouseAuthorityRepository) GetRestore(context.Context, clickhouse.RestoreKey) (clickhouse.RestoreRecord, bool, error) {
	return repository.restore, repository.restore.Key.RunID != "", nil
}
func (repository *clickHouseAuthorityRepository) ListRestores(context.Context, string) ([]clickhouse.RestoreRecord, error) {
	if repository.restore.Key.RunID == "" {
		return nil, nil
	}
	return []clickhouse.RestoreRecord{repository.restore}, nil
}

func TestServerAuthorizesClickHouseLedgerAndRestoreOnlyForExactCapability(t *testing.T) {
	resource, scope := clickHouseAuthorityFixture(t, clickhouse.OperationID)
	repository := &clickHouseAuthorityRepository{resource: resource}
	service := &Service{nodes: repository}

	entry := clickhouse.LedgerEntry{Key: clickhouse.LedgerKey{RunID: "run-1"}}
	if err := service.PutOperationLedger(context.Background(), "agent-1", "task-1", protocol.OperationLedgerPutRequest{Scope: scope, Entry: entry}); err != nil {
		t.Fatal(err)
	}
	record := clickhouse.RestoreRecord{Key: clickhouse.RestoreKey{RunID: "run-1", Database: "db", Table: "events"}}
	if err := service.PutOperationRestore(context.Background(), "agent-1", "task-1", protocol.OperationRestorePutRequest{Scope: scope, Record: record}); err != nil {
		t.Fatal(err)
	}
	got, err := service.GetOperationRestore(context.Background(), "agent-1", "task-1", protocol.OperationRestoreGetRequest{Scope: scope, Key: record.Key})
	if err != nil || !got.Found || got.Record.Key != record.Key {
		t.Fatalf("get=%#v err=%v", got, err)
	}
	listed, err := service.ListOperationRestores(context.Background(), "agent-1", "task-1", protocol.OperationRestoreListRunRequest{Scope: scope})
	if err != nil || len(listed.Records) != 1 || repository.ledgerPuts != 1 || repository.restorePuts != 1 {
		t.Fatalf("listed=%#v ledger_puts=%d restore_puts=%d err=%v", listed, repository.ledgerPuts, repository.restorePuts, err)
	}

	wrong, wrongScope := clickHouseAuthorityFixture(t, sysctlrepair.ID)
	repository.resource = wrong
	if err := service.PutOperationLedger(context.Background(), "agent-1", "task-1", protocol.OperationLedgerPutRequest{Scope: wrongScope, Entry: entry}); !IsValidationError(err) {
		t.Fatalf("cross-capability ledger error=%v", err)
	}
}

func clickHouseAuthorityFixture(t *testing.T, operationID string) (task.Resource, protocol.OperationActionScope) {
	t.Helper()
	target := operation.Target{Kind: operation.TargetNode, NodeID: "agent-1"}
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: operationID,
		RunID: "run-1",
		Action: task.OperationActionCreateRestorePoint,
		PlanDigest: "sha256:plan",
		Targets: []operation.Target{target},
		Plan: operation.Plan{SchemaVersion: "test.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.exec.v1", Payload: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource := task.Resource{
		Kind: task.KindOperationExecutionTask,
		Metadata: task.Metadata{ID: "task-1"},
		Spec: task.Spec{NodeID: "agent-1", OperationID: operationID, OperationExecution: &contract, ContractDigest: digest},
		Status: task.Status{Phase: task.PhaseRunning, ClaimID: "claim-1"},
	}
	scope := protocol.OperationActionScope{ClaimID: "claim-1", RunID: "run-1", Action: task.OperationActionCreateRestorePoint}
	return resource, scope
}
