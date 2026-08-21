package clickhouse

import (
	"context"
	"testing"
)

func TestReplicatedCommitTerminalStateSurvivesRepeatedRecovery(t *testing.T) {
	ledger, state, client, _, request := replicatedPartitionLabFixture(t)
	store := &failPutLedger{base: ledger, failOnPut: 2}
	first, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Commit(context.Background(), request); err == nil {
		t.Fatal("expected injected persistence error")
	}
	if client.replaces != 1 {
		t.Fatalf("replaces=%d", client.replaces)
	}

	for round := 1; round <= 2; round++ {
		recovery, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := recovery.Commit(context.Background(), request)
		if err != nil || result.Entry.State != LedgerCommitted || client.replaces != 1 {
			t.Fatalf("round=%d result=%#v err=%v replaces=%d", round, result, err, client.replaces)
		}
	}
}

func TestReplicatedRollbackTerminalStateSurvivesRepeatedRecovery(t *testing.T) {
	ledger, state, client, _, request := replicatedPartitionLabFixture(t)
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

	store := &failPutLedger{base: ledger, failOnPut: 2}
	first, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Rollback(context.Background(), request); err == nil {
		t.Fatal("expected injected persistence error")
	}
	if client.drops != 1 {
		t.Fatalf("drops=%d", client.drops)
	}

	for round := 1; round <= 2; round++ {
		recovery, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := recovery.Rollback(context.Background(), request)
		if err != nil || result.Entry.State != LedgerRolledBack || client.drops != 1 {
			t.Fatalf("round=%d result=%#v err=%v drops=%d", round, result, err, client.drops)
		}
	}
}
