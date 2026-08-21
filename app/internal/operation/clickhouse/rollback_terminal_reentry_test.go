package clickhouse

import (
	"context"
	"testing"
)

func TestAtomicRollbackTerminalStatesRejectReentry(t *testing.T) {
	for _, state := range []LedgerState{LedgerRollbackBlocked, LedgerRollbackFailed} {
		t.Run(string(state), func(t *testing.T) {
			ledger, entry, request := verifiedLedger(t)
			entry.State = state
			if err := ledger.Put(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			client := &commitQueryClient{}
			engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{}, allowCommitGuard{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Rollback(context.Background(), request); err == nil {
				t.Fatalf("terminal state %s unexpectedly accepted rollback reentry", state)
			}
			if _, err := engine.Commit(context.Background(), request); err == nil {
				t.Fatalf("terminal state %s unexpectedly accepted commit reentry", state)
			}
			if client.exchanges != 0 {
				t.Fatalf("terminal state %s issued %d EXCHANGE call(s)", state, client.exchanges)
			}
		})
	}
}

func TestReplicatedRollbackTerminalStatesRejectReentry(t *testing.T) {
	for _, state := range []LedgerState{LedgerRollbackBlocked, LedgerRollbackFailed} {
		t.Run(string(state), func(t *testing.T) {
			ledger, _, client, engine, request := replicatedPartitionLabFixture(t)
			entry, ok, err := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
			if err != nil || !ok {
				t.Fatalf("entry=%#v ok=%t err=%v", entry, ok, err)
			}
			entry.State = state
			if err := ledger.Put(context.Background(), entry); err != nil {
				t.Fatal(err)
			}
			if _, err := engine.Rollback(context.Background(), request); err == nil {
				t.Fatalf("terminal state %s unexpectedly accepted rollback reentry", state)
			}
			if _, err := engine.Commit(context.Background(), request); err == nil {
				t.Fatalf("terminal state %s unexpectedly accepted commit reentry", state)
			}
			if client.replaces != 0 || client.drops != 0 {
				t.Fatalf("terminal state %s issued calls: replace=%d drop=%d", state, client.replaces, client.drops)
			}
		})
	}
}
