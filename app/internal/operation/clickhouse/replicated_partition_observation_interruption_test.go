package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failObservationAfterWriteOnceClient struct {
	base           *replicaLabClient
	failedCommit   bool
	failedRollback bool
}

func (client *failObservationAfterWriteOnceClient) Query(ctx context.Context, request QueryRequest) (string, error) {
	if strings.Contains(request.Query, "FROM system.replicas") {
		if client.base.replaces > 0 && !client.failedCommit {
			client.failedCommit = true
			return "", errors.New("injected commit observation interruption")
		}
		if client.base.drops > 0 && !client.failedRollback {
			client.failedRollback = true
			return "", errors.New("injected rollback observation interruption")
		}
	}
	return client.base.Query(ctx, request)
}

func TestReplicatedCommitObservationInterruptionRecoversWithoutReplay(t *testing.T) {
	ledger, state, base, _, request := replicatedPartitionLabFixture(t)
	client := &failObservationAfterWriteOnceClient{base: base}
	engine, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "commit observation interruption") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerCommitUnknown || persisted.Checkpoint != "commit_unknown" || base.replaces != 1 {
		t.Fatalf("persisted=%#v ok=%t err=%v commit_calls=%d", persisted, ok, readErr, base.replaces)
	}

	recovery, err := NewReplicatedPartitionLabCommitEngine(ledger, base, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err = recovery.ReconcileCommit(context.Background(), request)
	if err != nil || result.Entry.State != LedgerCommitted || !result.RecoveredAmbiguous || base.replaces != 1 {
		t.Fatalf("recovery=%#v err=%v commit_calls=%d", result, err, base.replaces)
	}
}

func TestReplicatedRollbackObservationInterruptionRecoversWithoutReplay(t *testing.T) {
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

	client := &failObservationAfterWriteOnceClient{base: base, failedCommit: true}
	engine, err := NewReplicatedPartitionLabCommitEngine(ledger, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "rollback observation interruption") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), key)
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending || persisted.Checkpoint != "rollback_observation_pending" || base.drops != 1 {
		t.Fatalf("persisted=%#v ok=%t err=%v rollback_calls=%d", persisted, ok, readErr, base.drops)
	}

	recovery, err := NewReplicatedPartitionLabCommitEngine(ledger, base, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err = recovery.ReconcileRollback(context.Background(), request)
	if err != nil || result.Entry.State != LedgerRolledBack || !result.RecoveredAmbiguous || base.drops != 1 {
		t.Fatalf("recovery=%#v err=%v rollback_calls=%d", result, err, base.drops)
	}
}
