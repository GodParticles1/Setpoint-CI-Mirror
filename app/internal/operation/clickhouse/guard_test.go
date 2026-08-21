package clickhouse

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func TestLeaseCommitGuardRequiresRunOwnershipAndResource(t *testing.T) {
	target, err := ClickHouseTableLockTarget("site-1", "", "db", "events")
	if err != nil { t.Fatal(err) }
	key, err := operation.ResourceLockKey(target)
	if err != nil { t.Fatal(err) }
	now := time.Now().UTC()
	lease := operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: key}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	guard, err := NewLeaseCommitGuard(lease, key)
	if err != nil { t.Fatal(err) }
	guard.now = func() time.Time { return now }
	staging, err := BuildStagingTableName("run-1", "events")
	if err != nil { t.Fatal(err) }
	request := CommitGuardRequest{RunID: "run-1", Database: "db", TargetTable: "events", StagingTable: staging}
	if err := guard.Verify(context.Background(), request); err != nil { t.Fatal(err) }
	request.RunID = "run-2"
	if err := guard.Verify(context.Background(), request); err == nil { t.Fatal("wrong run owner accepted") }
}

func TestLeaseCommitGuardRejectsForeignStagingName(t *testing.T) {
	target, err := ClickHouseTableLockTarget("site-1", "", "db", "events")
	if err != nil { t.Fatal(err) }
	key, err := operation.ResourceLockKey(target)
	if err != nil { t.Fatal(err) }
	now := time.Now().UTC()
	lease := operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: key}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	guard, err := NewLeaseCommitGuard(lease, key)
	if err != nil { t.Fatal(err) }
	guard.now = func() time.Time { return now }
	foreign, err := BuildStagingTableName("other-run", "events")
	if err != nil { t.Fatal(err) }
	if err := guard.Verify(context.Background(), CommitGuardRequest{RunID: "run-1", Database: "db", TargetTable: "events", StagingTable: foreign}); err == nil {
		t.Fatal("foreign staging name accepted")
	}
}

func TestLeaseCommitGuardRejectsExpiredLease(t *testing.T) {
	now := time.Now().UTC()
	lease := operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: "resource"}}, AcquiredAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	if _, err := NewLeaseCommitGuard(lease, "resource"); err == nil { t.Fatal("expired lease accepted") }
}
