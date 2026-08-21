package clickhouse

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func TestAtomicExchangeRollbackRejectsForeignStagingBeforeObservationOrExchange(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	owned, err := BuildStagingTableName("run-1", "events")
	if err != nil {
		t.Fatal(err)
	}
	entry.StagingTable = owned
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	target, err := ClickHouseTableLockTarget("site-1", "", "db", "events")
	if err != nil {
		t.Fatal(err)
	}
	resourceKey, err := operation.ResourceLockKey(target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	lease := operation.LockLease{
		ID: "lease-1", OwnerID: "run-1",
		Resources: []operation.LockResource{{Key: resourceKey}},
		AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	guard, err := NewLeaseCommitGuard(lease, resourceKey)
	if err != nil {
		t.Fatal(err)
	}
	guard.now = func() time.Time { return now }

	foreign, err := BuildStagingTableName("another-run", "events")
	if err != nil {
		t.Fatal(err)
	}
	request.Chunk.StagingTable = foreign

	client := &commitQueryClient{}
	verifier := &sequenceVerifier{}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, guard)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil {
		t.Fatal("rollback unexpectedly accepted foreign staging")
	}
	if result.Entry.State != LedgerCommitted {
		t.Fatalf("state=%s want=%s", result.Entry.State, LedgerCommitted)
	}
	if client.exchanges != 0 {
		t.Fatalf("foreign staging caused %d EXCHANGE calls", client.exchanges)
	}
	if verifier.index != 0 {
		t.Fatalf("foreign staging reached fingerprint observation: calls=%d", verifier.index)
	}
}
