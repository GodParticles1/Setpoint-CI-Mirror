package clickhouse

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func TestRestorePointMaterializesPreparedPlanToActualRun(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &noOpStaging{}
	query := &commitQueryClient{}
	verifier := &restoreVerifier{fingerprint: DataFingerprint{}}
	commit, err := NewAtomicExchangeCommitEngine(ledger, query, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	targetTable := Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}}
	provider, _, _ := newTestRestoreProvider(t, ledger, stage, verifier, commit, targetTable)

	preparedStaging, err := BuildStagingTableName("prepared", "events")
	if err != nil {
		t.Fatal(err)
	}
	plan := ExchangeRestorePlan{
		Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}},
		Items: []ExchangeRestoreItem{{
			Chunk:       TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: preparedStaging, Sequence: 1},
			TargetTable: targetTable,
		}},
	}
	artifact, err := EncodeExchangeRestorePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	point, err := provider.Create(context.Background(), operation.RestorePointRequest{
		OperationID: OperationID,
		RunID:       "run-actual",
		Targets:     []operation.Target{{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}},
		Plan:        operation.Plan{Execution: artifact},
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := provider.decodeManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Plan.Items) != 1 || len(manifest.Baselines) != 1 {
		t.Fatalf("manifest=%#v", manifest)
	}
	item := manifest.Plan.Items[0]
	expectedStaging, err := BuildStagingTableName("run-actual", "events")
	if err != nil {
		t.Fatal(err)
	}
	if item.Chunk.RunID != "run-actual" || item.Chunk.StagingTable != expectedStaging {
		t.Fatalf("materialized chunk=%#v expected_staging=%s", item.Chunk, expectedStaging)
	}
	if manifest.Baselines[0].Key != ledgerKeyForChunk(item.Chunk) || manifest.Baselines[0].Key.RunID != "run-actual" {
		t.Fatalf("baseline=%#v chunk=%#v", manifest.Baselines[0], item.Chunk)
	}

	entry := LedgerEntry{
		Key:          ledgerKeyForChunk(item.Chunk),
		Strategy:     item.Chunk.Strategy,
		State:        LedgerCommitted,
		Attempt:      1,
		Checkpoint:   "exchange_committed",
		StagingTable: item.Chunk.StagingTable,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	verification, err := provider.VerifyRestored(context.Background(), point, operation.RollbackResult{Restored: true})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Passed {
		t.Fatalf("restore verification ignored actual-run ledger state: %#v", verification)
	}
}
