package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
)

func TestOperationLockManagerPersistsOwnershipRenewalExpiryAndRelease(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	now := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
	store, err := open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	request := operation.LockRequest{OwnerID: "run-a", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|z"}, {Key: "table|a"}, {Key: "table|a"}}}
	lease, err := store.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Resources) != 2 || lease.Resources[0].Key != "table|a" || lease.Resources[1].Key != "table|z" {
		t.Fatalf("normalized lease=%#v", lease)
	}
	retry, err := store.Acquire(ctx, request)
	if err != nil || retry.ID != lease.ID || !retry.ExpiresAt.Equal(lease.ExpiresAt) {
		t.Fatalf("same-owner acquisition was not idempotent: retry=%#v err=%v", retry, err)
	}
	if _, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-a", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|other"}}}); err == nil {
		t.Fatal("same owner must not silently replace its resource set")
	}
	if _, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-b", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|a"}}}); err == nil {
		t.Fatal("overlapping live resource lease must fail")
	}

	now = now.Add(30 * time.Second)
	renewed, err := store.Renew(ctx, lease, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("renewed expiry=%s", renewed.ExpiresAt)
	}
	if _, err := store.Renew(ctx, lease, 2*time.Minute); err == nil {
		t.Fatal("stale lease snapshot must not renew current ownership")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = open(ctx, path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := store.Acquire(ctx, request)
	if err != nil || reopened.ID != renewed.ID || !reopened.ExpiresAt.Equal(renewed.ExpiresAt) {
		t.Fatalf("same-owner acquisition did not recover persisted lease: lease=%#v err=%v", reopened, err)
	}
	if _, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-b", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|z"}}}); err == nil {
		t.Fatal("lease ownership must survive SQLite reopen")
	}

	now = renewed.ExpiresAt
	replacement, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-b", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|a"}, {Key: "table|z"}}})
	if err != nil {
		t.Fatalf("expired lease was not reclaimed: %v", err)
	}
	if replacement.ID == renewed.ID || replacement.OwnerID != "run-b" {
		t.Fatalf("replacement lease=%#v", replacement)
	}
	if err := store.Release(ctx, renewed); err != nil {
		t.Fatalf("release of already-reclaimed lease should be idempotent: %v", err)
	}
	if _, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-c", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|a"}}}); err == nil {
		t.Fatal("stale release removed replacement ownership")
	}

	wrongOwner := replacement
	wrongOwner.OwnerID = "run-c"
	if err := store.Release(ctx, wrongOwner); err == nil {
		t.Fatal("different owner must not release current lease")
	}
	if err := store.Release(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(ctx, replacement); err != nil {
		t.Fatalf("release must be idempotent: %v", err)
	}
	if _, err := store.Acquire(ctx, operation.LockRequest{OwnerID: "run-c", TTL: time.Minute, Resources: []operation.LockResource{{Key: "table|a"}}}); err != nil {
		t.Fatalf("released resource cannot be reacquired: %v", err)
	}
}
