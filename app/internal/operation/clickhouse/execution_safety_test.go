package clickhouse

import (
	"context"
	"testing"
	"time"
)

func TestNativeExecutionNeverRecreatesStagingForAmbiguousCommit(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &fakeStaging{}
	transport := &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	entry := LedgerEntry{Key: ledgerKeyForChunk(request.Chunk), Strategy: StrategyNativeStream, State: LedgerCommitUnknown, Attempt: 1, StagingTable: request.Chunk.StagingTable, Source: fingerprint, Target: fingerprint, UpdatedAt: time.Now()}
	if err := ledger.Put(context.Background(), entry); err != nil { t.Fatal(err) }
	result, err := engine.Execute(context.Background(), request)
	if err == nil { t.Fatal("commit_unknown transfer retry unexpectedly succeeded") }
	if result.Entry.State != LedgerCommitUnknown || stage.recreate != 0 || transport.calls != 0 { t.Fatalf("result=%#v recreate=%d transfers=%d", result, stage.recreate, transport.calls) }
}
