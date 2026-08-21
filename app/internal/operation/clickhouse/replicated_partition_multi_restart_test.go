package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failReplicaObservationOnceClient struct {
	base   QueryClient
	failed bool
}

func (client *failReplicaObservationOnceClient) Query(ctx context.Context, request QueryRequest) (string, error) {
	if !client.failed && strings.Contains(request.Query, "FROM system.replicas") {
		client.failed = true
		return "", errors.New("injected replica observation failure")
	}
	return client.base.Query(ctx, request)
}

func TestReplicatedCommitMultiRestartObservationFailureNeverReplaysReplace(t *testing.T) {
	ledger, state, client, engine, request := replicatedPartitionLabFixture(t)
	client.replaceMode = "partial"

	first, err := engine.Commit(context.Background(), request)
	if err == nil || first.Entry.State != LedgerReplicasConverging || first.Entry.Checkpoint != "replicas_converging" || client.replaces != 1 {
		t.Fatalf("first=%#v err=%v replaces=%d", first, err, client.replaces)
	}

	flaky := &failReplicaObservationOnceClient{base: client}
	restart1, err := NewReplicatedPartitionLabCommitEngine(ledger, flaky, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := restart1.ReconcileCommit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "injected replica observation failure") || client.replaces != 1 {
		t.Fatalf("failed=%#v err=%v replaces=%d", failed, err, client.replaces)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerReplicasConverging || persisted.Checkpoint != "replicas_converging" {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}

	restart2, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := restart2.ReconcileCommit(context.Background(), request)
	if err == nil || pending.Entry.State != LedgerReplicasConverging || client.replaces != 1 {
		t.Fatalf("pending=%#v err=%v replaces=%d", pending, err, client.replaces)
	}

	state.setAll(state.source, 1)
	restart3, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restart3.ReconcileCommit(context.Background(), request)
	if err != nil || completed.Entry.State != LedgerCommitted || !completed.RecoveredAmbiguous || client.replaces != 1 {
		t.Fatalf("completed=%#v err=%v replaces=%d", completed, err, client.replaces)
	}
}

func TestReplicatedRollbackMultiRestartObservationFailureNeverReplaysDrop(t *testing.T) {
	ledger, state, client, engine, request := replicatedPartitionLabFixture(t)
	state.setAll(state.source, 1)
	entry, ok, err := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if err != nil || !ok {
		t.Fatalf("entry=%#v ok=%t err=%v", entry, ok, err)
	}
	entry.State = LedgerCommitted
	entry.Checkpoint = "replicas_verified_committed"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client.dropMode = "partial"

	first, err := engine.Rollback(context.Background(), request)
	if err == nil || first.Entry.State != LedgerRollbackPending || first.Entry.Checkpoint != "rollback_replicas_converging" || client.drops != 1 {
		t.Fatalf("first=%#v err=%v drops=%d", first, err, client.drops)
	}

	flaky := &failReplicaObservationOnceClient{base: client}
	restart1, err := NewReplicatedPartitionLabCommitEngine(ledger, flaky, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := restart1.ReconcileRollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "injected replica observation failure") || client.drops != 1 {
		t.Fatalf("failed=%#v err=%v drops=%d", failed, err, client.drops)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending || persisted.Checkpoint != "rollback_replicas_converging" {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}

	restart2, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := restart2.ReconcileRollback(context.Background(), request)
	if err == nil || pending.Entry.State != LedgerRollbackPending || client.drops != 1 {
		t.Fatalf("pending=%#v err=%v drops=%d", pending, err, client.drops)
	}

	state.setAll(DataFingerprint{}, 0)
	restart3, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restart3.ReconcileRollback(context.Background(), request)
	if err != nil || completed.Entry.State != LedgerRolledBack || !completed.RecoveredAmbiguous || client.drops != 1 {
		t.Fatalf("completed=%#v err=%v drops=%d", completed, err, client.drops)
	}
}
