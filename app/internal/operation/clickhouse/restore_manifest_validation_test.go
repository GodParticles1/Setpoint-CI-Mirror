package clickhouse

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func restoreManifestFixture(t *testing.T) (*ExchangeRestoreProvider, operation.RestorePoint, ExchangeRestoreManifest) {
	t.Helper()
	ledger := newMemoryLedger()
	verifier := &restoreVerifier{fingerprint: DataFingerprint{}}
	commit, err := NewAtomicExchangeCommitEngine(ledger, &commitQueryClient{}, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	targetTable := Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}}
	provider, _, _ := newTestRestoreProvider(t, ledger, &noOpStaging{}, verifier, commit, targetTable)
	staging, err := BuildStagingTableName("prepared", "events")
	if err != nil {
		t.Fatal(err)
	}
	plan := ExchangeRestorePlan{
		Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}},
		Items: []ExchangeRestoreItem{{
			Chunk:       TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: staging, Sequence: 1},
			TargetTable: targetTable,
		}},
	}
	artifact, err := EncodeExchangeRestorePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	point, err := provider.Create(context.Background(), operation.RestorePointRequest{
		OperationID: OperationID,
		RunID:       "run-manifest",
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
	return provider, point, manifest
}

func corruptRestoreManifest(t *testing.T, point operation.RestorePoint, manifest ExchangeRestoreManifest) operation.RestorePoint {
	t.Helper()
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	point.Manifest.Payload = payload
	return point
}

func TestRestoreManifestRejectsRunAndStagingOwnershipCorruption(t *testing.T) {
	provider, point, manifest := restoreManifestFixture(t)

	runCorrupt := manifest
	runCorrupt.Plan.Items = append([]ExchangeRestoreItem(nil), manifest.Plan.Items...)
	runCorrupt.Plan.Items[0].Chunk.RunID = "other-run"
	if _, err := provider.Verify(context.Background(), corruptRestoreManifest(t, point, runCorrupt)); err == nil || !strings.Contains(err.Error(), "run ID") {
		t.Fatalf("corrupted run ownership accepted: %v", err)
	}

	stagingCorrupt := manifest
	stagingCorrupt.Plan.Items = append([]ExchangeRestoreItem(nil), manifest.Plan.Items...)
	stagingCorrupt.Plan.Items[0].Chunk.StagingTable = "foreign_staging"
	if _, err := provider.Verify(context.Background(), corruptRestoreManifest(t, point, stagingCorrupt)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("corrupted staging ownership accepted: %v", err)
	}
}

func TestRestoreManifestRejectsBaselineAndTargetCorruption(t *testing.T) {
	provider, point, manifest := restoreManifestFixture(t)

	baselineCorrupt := manifest
	baselineCorrupt.Baselines = append([]ExchangeRestoreBaseline(nil), manifest.Baselines...)
	baselineCorrupt.Baselines[0].Key.RunID = "other-run"
	if _, err := provider.Verify(context.Background(), corruptRestoreManifest(t, point, baselineCorrupt)); err == nil || !strings.Contains(err.Error(), "missing baseline") {
		t.Fatalf("corrupted baseline ownership accepted: %v", err)
	}

	tokenCorrupt := manifest
	tokenCorrupt.Baselines = append([]ExchangeRestoreBaseline(nil), manifest.Baselines...)
	tokenCorrupt.Baselines[0].OwnershipToken = strings.Repeat("10", restoreOwnershipTokenBytes)
	if _, err := provider.Verify(context.Background(), corruptRestoreManifest(t, point, tokenCorrupt)); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("corrupted restore ownership token accepted: %v", err)
	}

	targetCorrupt := point
	targetCorrupt.Targets = []operation.Target{{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.other"}}
	if _, err := provider.Verify(context.Background(), targetCorrupt); err == nil || !strings.Contains(err.Error(), "outside restore point targets") {
		t.Fatalf("out-of-target manifest accepted: %v", err)
	}
}
