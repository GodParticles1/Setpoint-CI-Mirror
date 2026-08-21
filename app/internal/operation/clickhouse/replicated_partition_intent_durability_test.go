package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type replicatedIntentCheckingClient struct {
	base         *replicaLabClient
	ledger       *memoryLedger
	key          LedgerKey
	commitSeen   bool
	rollbackSeen bool
}

func (client *replicatedIntentCheckingClient) Query(ctx context.Context, request QueryRequest) (string, error) {
	if strings.HasPrefix(request.Query, "ALTER TABLE") && strings.Contains(request.Query, " REPLACE PARTITION ") {
		entry, ok, err := client.ledger.Get(ctx, client.key)
		if err != nil {
			return "", err
		}
		if !ok || entry.State != LedgerCommitPending || entry.Checkpoint != "replace_pending" {
			return "", errors.New("replace intent was not durably recorded")
		}
		client.commitSeen = true
	}
	if strings.HasPrefix(request.Query, "ALTER TABLE") && strings.Contains(request.Query, " DROP PARTITION ") {
		entry, ok, err := client.ledger.Get(ctx, client.key)
		if err != nil {
			return "", err
		}
		if !ok || entry.State != LedgerRollbackPending || entry.Checkpoint != "drop_partition_pending" {
			return "", errors.New("rollback intent was not durably recorded")
		}
		client.rollbackSeen = true
	}
	return client.base.Query(ctx, request)
}

func TestReplicatedCommitPersistsIntentBeforeReplace(t *testing.T) {
	ledger, state, base, _, request := replicatedPartitionLabFixture(t)
	client := &replicatedIntentCheckingClient{base: base, ledger: ledger, key: ledgerKeyForChunk(request.Chunk)}
	engine, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !client.commitSeen || base.replaces != 1 || result.Entry.State != LedgerCommitted {
		t.Fatalf("seen=%t replaces=%d result=%#v", client.commitSeen, base.replaces, result)
	}
}

func TestReplicatedCommitIntentPersistenceFailureBlocksReplace(t *testing.T) {
	ledger, state, base, _, request := replicatedPartitionLabFixture(t)
	store := &failPutLedger{base: ledger, failOnPut: 1}
	engine, err := NewReplicatedPartitionLabCommitEngine(store, base, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil {
		t.Fatalf("result=%#v", result)
	}
	if base.replaces != 0 {
		t.Fatalf("REPLACE calls=%d", base.replaces)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerVerified {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestReplicatedRollbackPersistsIntentBeforeDrop(t *testing.T) {
	ledger, state, base, _, request := replicatedPartitionLabFixture(t)
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
	client := &replicatedIntentCheckingClient{base: base, ledger: ledger, key: key}
	engine, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !client.rollbackSeen || base.drops != 1 || result.Entry.State != LedgerRolledBack {
		t.Fatalf("seen=%t drops=%d result=%#v", client.rollbackSeen, base.drops, result)
	}
}

func TestReplicatedRollbackIntentPersistenceFailureBlocksDrop(t *testing.T) {
	ledger, state, base, _, request := replicatedPartitionLabFixture(t)
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
	store := &failPutLedger{base: ledger, failOnPut: 1}
	engine, err := NewReplicatedPartitionLabCommitEngine(store, base, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil {
		t.Fatalf("result=%#v", result)
	}
	if base.drops != 0 {
		t.Fatalf("DROP calls=%d", base.drops)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), key)
	if readErr != nil || !ok || persisted.State != LedgerCommitted {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}
