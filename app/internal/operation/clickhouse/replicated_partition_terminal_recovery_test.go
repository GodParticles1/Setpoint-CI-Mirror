package clickhouse

import (
	"context"
	"testing"
)

func TestReplicatedCommittedTerminalStateRequiresReplicaProof(t *testing.T) {
	ledger, state, client, engine, request := replicatedPartitionLabFixture(t)
	state.setAll(state.source, 1)
	key := ledgerKeyForChunk(request.Chunk)
	entry, ok, err := ledger.Get(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("entry=%#v ok=%t err=%v", entry, ok, err)
	}
	entry.State = LedgerCommitted
	entry.Checkpoint = "replicas_verified_committed"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	state.set("r3", DataFingerprint{}, 0)
	result, err := engine.Commit(context.Background(), request)
	if err == nil {
		t.Fatalf("drifted committed state unexpectedly accepted: %#v", result)
	}
	if result.Entry.State != LedgerCommitted || client.replaces != 0 {
		t.Fatalf("result=%#v replaces=%d", result, client.replaces)
	}
}

func TestReplicatedRolledBackTerminalStateRequiresReplicaProof(t *testing.T) {
	ledger, state, client, engine, request := replicatedPartitionLabFixture(t)
	state.setAll(DataFingerprint{}, 0)
	key := ledgerKeyForChunk(request.Chunk)
	entry, ok, err := ledger.Get(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("entry=%#v ok=%t err=%v", entry, ok, err)
	}
	entry.State = LedgerRolledBack
	entry.Checkpoint = "rollback_reconciled_absent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	state.set("r2", state.source, 1)
	result, err := engine.Rollback(context.Background(), request)
	if err == nil {
		t.Fatalf("drifted rolled-back state unexpectedly accepted: %#v", result)
	}
	if result.Entry.State != LedgerRolledBack || client.drops != 0 {
		t.Fatalf("result=%#v drops=%d", result, client.drops)
	}
}
