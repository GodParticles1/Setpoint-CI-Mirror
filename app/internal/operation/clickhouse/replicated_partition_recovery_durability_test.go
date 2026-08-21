package clickhouse

import (
	"context"
	"testing"
)

func TestReplicatedCommitFinalPersistenceFailureRecoversWithoutReplaceReplay(t *testing.T) {
	ledger, state, client, _, request := replicatedPartitionLabFixture(t)
	store := &failPutLedger{base: ledger, failOnPut: 2}
	engine, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := engine.Commit(context.Background(), request); err == nil {
		t.Fatal("injected final replicated commit persistence failure unexpectedly succeeded")
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
	if persisted.State != LedgerCommitPending || persisted.Checkpoint != "replace_pending" || client.replaces != 1 {
		t.Fatalf("persisted=%#v replaces=%d", persisted, client.replaces)
	}

	recovery, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovery.Commit(context.Background(), request)
	if err != nil {
		t.Fatalf("recovery result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerCommitted || !result.RecoveredAmbiguous || client.replaces != 1 {
		t.Fatalf("recovery=%#v replaces=%d", result, client.replaces)
	}
}

func TestReplicatedRollbackFinalPersistenceFailureRecoversWithoutDropReplay(t *testing.T) {
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
	engine, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Rollback(context.Background(), request); err == nil {
		t.Fatal("injected final replicated rollback persistence failure unexpectedly succeeded")
	}
	persisted, ok, readErr := ledger.Get(context.Background(), key)
	if readErr != nil || !ok {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
	if persisted.State != LedgerRollbackPending || persisted.Checkpoint != "drop_partition_pending" || client.drops != 1 {
		t.Fatalf("persisted=%#v drops=%d", persisted, client.drops)
	}

	recovery, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovery.Rollback(context.Background(), request)
	if err != nil {
		t.Fatalf("recovery result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerRolledBack || client.drops != 1 || result.Replicas.Absent != 3 {
		t.Fatalf("recovery=%#v drops=%d", result, client.drops)
	}
}
