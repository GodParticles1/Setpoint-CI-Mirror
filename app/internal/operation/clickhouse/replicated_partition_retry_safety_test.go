package clickhouse

import (
	"context"
	"testing"
	"time"
)

func TestNativeExecutionBlocksTransferRetryDuringReplicatedCommitReconciliation(t *testing.T) {
	for _, state := range []LedgerState{LedgerCommitPending, LedgerReplicasConverging, LedgerCommitUnknown} {
		t.Run(string(state), func(t *testing.T) {
			ledger := newMemoryLedger()
			stage := &fakeStaging{}
			transport := &fakeTransport{}
			fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
			engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
			entry := LedgerEntry{
				Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1},
				Strategy: StrategyNativeStream, State: state, Attempt: 1, StagingTable: request.Chunk.StagingTable,
				Source: fingerprint, Target: fingerprint, UpdatedAt: time.Now().UTC(),
			}
			if err := ledger.Put(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Execute(context.Background(), request); err == nil {
				t.Fatalf("transfer retry unexpectedly allowed in %s", state)
			}
			if stage.recreate != 0 || transport.calls != 0 {
				t.Fatalf("state=%s recreate=%d transport=%d", state, stage.recreate, transport.calls)
			}
		})
	}
}
