package clickhouse

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"setpoint/internal/operation"
)

type staticLease struct { lease operation.LockLease }
func (lease staticLease) Current() operation.LockLease { return lease.lease }
func (lease staticLease) Validate(now time.Time) error { return operation.ValidateLease(lease.lease, now) }

type definitionNativeTransport struct{}
func (definitionNativeTransport) Transfer(context.Context, NativeTransferRequest) (NativeTransferResult, error) {
	return NativeTransferResult{}, nil
}

func TestDefinitionMaterializesRunOwnedStagingAndValidatesLease(t *testing.T) {
	ledger := newMemoryLedger()
	query := &commitQueryClient{}
	stage := &noOpStaging{}
	transport := definitionNativeTransport{}
	verifier := &restoreVerifier{fingerprint: DataFingerprint{Rows: 0}}
	definition, err := NewDefinition(query, ledger, stage, transport, verifier)
	if err != nil { t.Fatal(err) }
	plan := ExchangeRestorePlan{Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}, Items: []ExchangeRestoreItem{{Chunk: TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_template", Sequence: 1}, TargetTable: Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}}}}}
	payload, _ := json.Marshal(ExchangeRestoreManifest{Plan: plan})
	point := operation.RestorePoint{RunID: "run-42", Manifest: operation.Artifact{SchemaVersion: ExchangeRestoreManifestSchema, Payload: payload}}
	materialized, err := definition.materializedPlan(point)
	if err != nil { t.Fatal(err) }
	if materialized.Items[0].Chunk.RunID != "run-42" { t.Fatalf("run=%q", materialized.Items[0].Chunk.RunID) }
	if materialized.Items[0].Chunk.StagingTable == "spmig_events_template" { t.Fatal("staging table was not materialized from run ID") }

	target := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}
	key, _ := operation.ResourceLockKey(target)
	now := time.Now().UTC()
	lease := staticLease{lease: operation.LockLease{ID: "lease-1", OwnerID: "run-42", Resources: []operation.LockResource{{Key: key}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}}
	if err := validateLeaseForItem(lease, "run-42", materialized.Items[0]); err != nil { t.Fatal(err) }
	if err := validateLeaseForItem(lease, "other-run", materialized.Items[0]); err == nil { t.Fatal("wrong owner lease accepted") }
}
