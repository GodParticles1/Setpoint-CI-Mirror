package clickhouse

import (
	"context"
	"testing"
	"time"
)

type stagingWithoutInspector struct{}
func (stagingWithoutInspector) Recreate(context.Context, Endpoint, string, string, string) error { return nil }
func (stagingWithoutInspector) Drop(context.Context, Endpoint, string, string) error { return nil }

func TestNativeReconcileBlocksWhenStagingExistenceCannotBeProven(t *testing.T) {
	ledger := newMemoryLedger()
	transport := &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	engine, request := nativeExecutionFixture(t, ledger, stagingWithoutInspector{}, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	entry := LedgerEntry{
		Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1},
		Strategy: StrategyNativeStream,
		State: LedgerStaging,
		Attempt: 1,
		StagingTable: request.Chunk.StagingTable,
		Source: fingerprint,
		UpdatedAt: time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil { t.Fatal(err) }
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("reconcile unexpectedly allowed replay without staging inspector")
	}
	if transport.calls != 0 {
		t.Fatalf("transport replayed without ownership evidence: calls=%d", transport.calls)
	}
}

func TestNativeReconcileBlocksLedgerIdentityMismatch(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &fakeStaging{exists: true}
	transport := &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	entry := LedgerEntry{
		Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1},
		Strategy: StrategyNativeStream,
		State: LedgerStaging,
		Attempt: 1,
		StagingTable: "spmig_other_deadbeef0000",
		Source: fingerprint,
		UpdatedAt: time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil { t.Fatal(err) }
	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("reconcile unexpectedly accepted mismatched staging ownership")
	}
	if transport.calls != 0 || stage.recreate != 0 {
		t.Fatalf("unsafe replay after ownership mismatch: transport=%d recreate=%d", transport.calls, stage.recreate)
	}
}
