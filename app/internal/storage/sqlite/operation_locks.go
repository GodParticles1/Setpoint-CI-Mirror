package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"setpoint/internal/operation"
)

func (store *Store) Acquire(ctx context.Context, request operation.LockRequest) (operation.LockLease, error) {
	normalized, err := operation.NormalizeLockRequest(request)
	if err != nil {
		return operation.LockLease{}, err
	}
	now := store.now().UTC()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.LockLease{}, fmt.Errorf("begin operation lock acquisition: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := purgeExpiredOperationLeases(ctx, transaction, now); err != nil {
		return operation.LockLease{}, err
	}

	owned, found, err := readOperationLeaseByOwner(ctx, transaction, normalized.OwnerID)
	if err != nil {
		return operation.LockLease{}, err
	}
	if found {
		if reflect.DeepEqual(owned.Resources, normalized.Resources) {
			return owned, nil
		}
		return operation.LockLease{}, errors.New("operation lock owner already holds a different resource set")
	}

	for _, resource := range normalized.Resources {
		var leaseID string
		err := transaction.QueryRowContext(ctx, `SELECT lease_id FROM operation_lock_resources WHERE resource_key = ?`, resource.Key).Scan(&leaseID)
		if err == nil {
			return operation.LockLease{}, fmt.Errorf("operation lock resource %q is already leased by %s", resource.Key, leaseID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return operation.LockLease{}, fmt.Errorf("check operation lock resource %q: %w", resource.Key, err)
		}
	}
	leaseID, err := newOperationLeaseID()
	if err != nil {
		return operation.LockLease{}, err
	}
	lease := operation.LockLease{ID: leaseID, OwnerID: normalized.OwnerID, Resources: normalized.Resources, AcquiredAt: now, ExpiresAt: now.Add(normalized.TTL)}
	resourcesJSON, err := json.Marshal(lease.Resources)
	if err != nil {
		return operation.LockLease{}, fmt.Errorf("encode operation lock resources: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operation_leases(id, owner_id, resources_json, acquired_at, expires_at) VALUES(?, ?, ?, ?, ?)`,
		lease.ID, lease.OwnerID, string(resourcesJSON), formatTime(lease.AcquiredAt), formatTime(lease.ExpiresAt)); err != nil {
		return operation.LockLease{}, fmt.Errorf("insert operation lease: %w", err)
	}
	for _, resource := range lease.Resources {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO operation_lock_resources(resource_key, lease_id) VALUES(?, ?)`, resource.Key, lease.ID); err != nil {
			return operation.LockLease{}, fmt.Errorf("reserve operation lock resource %q: %w", resource.Key, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return operation.LockLease{}, fmt.Errorf("commit operation lock acquisition: %w", err)
	}
	return lease, nil
}

func (store *Store) Renew(ctx context.Context, lease operation.LockLease, ttl time.Duration) (operation.LockLease, error) {
	if ttl <= 0 {
		return operation.LockLease{}, errors.New("lock renewal TTL must be positive")
	}
	now := store.now().UTC()
	if err := operation.ValidateLease(lease, now); err != nil {
		return operation.LockLease{}, err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.LockLease{}, fmt.Errorf("begin operation lease renewal: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	persisted, found, err := readOperationLease(ctx, transaction, lease.ID)
	if err != nil {
		return operation.LockLease{}, err
	}
	if !found {
		return operation.LockLease{}, errors.New("operation lease no longer exists")
	}
	if persisted.OwnerID != lease.OwnerID || !persisted.AcquiredAt.Equal(lease.AcquiredAt) || !persisted.ExpiresAt.Equal(lease.ExpiresAt) || !reflect.DeepEqual(persisted.Resources, lease.Resources) {
		return operation.LockLease{}, errors.New("operation lease snapshot is stale or ownership changed")
	}
	if err := operation.ValidateLease(persisted, now); err != nil {
		return operation.LockLease{}, err
	}
	persisted.ExpiresAt = now.Add(ttl)
	if _, err := transaction.ExecContext(ctx, `UPDATE operation_leases SET expires_at = ? WHERE id = ?`, formatTime(persisted.ExpiresAt), persisted.ID); err != nil {
		return operation.LockLease{}, fmt.Errorf("renew operation lease: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return operation.LockLease{}, fmt.Errorf("commit operation lease renewal: %w", err)
	}
	return persisted, nil
}

func (store *Store) Release(ctx context.Context, lease operation.LockLease) error {
	if lease.ID == "" || lease.OwnerID == "" {
		return errors.New("lock lease identity is incomplete")
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin operation lease release: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	persisted, found, err := readOperationLease(ctx, transaction, lease.ID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if persisted.OwnerID != lease.OwnerID {
		return errors.New("operation lease is owned by a different run")
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM operation_leases WHERE id = ? AND owner_id = ?`, lease.ID, lease.OwnerID); err != nil {
		return fmt.Errorf("release operation lease: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit operation lease release: %w", err)
	}
	return nil
}

func (store *Store) CurrentLeaseByOwner(ctx context.Context, ownerID string) (operation.LockLease, bool, error) {
	if ownerID == "" {
		return operation.LockLease{}, false, errors.New("lock owner is required")
	}
	now := store.now().UTC()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operation.LockLease{}, false, fmt.Errorf("begin authoritative operation lease read: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := purgeExpiredOperationLeases(ctx, transaction, now); err != nil {
		return operation.LockLease{}, false, err
	}
	lease, found, err := readOperationLeaseByOwner(ctx, transaction, ownerID)
	if err != nil {
		return operation.LockLease{}, false, err
	}
	if found {
		if err := operation.ValidateLease(lease, now); err != nil {
			return operation.LockLease{}, false, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return operation.LockLease{}, false, fmt.Errorf("commit authoritative operation lease read: %w", err)
	}
	return lease, found, nil
}

func purgeExpiredOperationLeases(ctx context.Context, transaction *sql.Tx, now time.Time) error {
	rows, err := transaction.QueryContext(ctx, `SELECT id, expires_at FROM operation_leases`)
	if err != nil {
		return fmt.Errorf("list operation leases for expiry: %w", err)
	}
	var expired []string
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return fmt.Errorf("scan operation lease expiry: %w", err)
		}
		expiresAt, err := parseTime(raw, "operation lease expiry")
		if err != nil {
			rows.Close()
			return err
		}
		if !now.Before(expiresAt) {
			expired = append(expired, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range expired {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM operation_leases WHERE id = ?`, id); err != nil {
			return fmt.Errorf("purge expired operation lease %s: %w", id, err)
		}
	}
	return nil
}

func readOperationLease(ctx context.Context, transaction *sql.Tx, leaseID string) (operation.LockLease, bool, error) {
	return scanOperationLease(transaction.QueryRowContext(ctx, `SELECT id, owner_id, resources_json, acquired_at, expires_at FROM operation_leases WHERE id = ?`, leaseID))
}

func readOperationLeaseByOwner(ctx context.Context, transaction *sql.Tx, ownerID string) (operation.LockLease, bool, error) {
	return scanOperationLease(transaction.QueryRowContext(ctx, `SELECT id, owner_id, resources_json, acquired_at, expires_at FROM operation_leases WHERE owner_id = ?`, ownerID))
}

func scanOperationLease(row *sql.Row) (operation.LockLease, bool, error) {
	var lease operation.LockLease
	var resourcesJSON, acquiredAt, expiresAt string
	err := row.Scan(&lease.ID, &lease.OwnerID, &resourcesJSON, &acquiredAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.LockLease{}, false, nil
	}
	if err != nil {
		return operation.LockLease{}, false, fmt.Errorf("read operation lease: %w", err)
	}
	lease, err = decodeOperationLease(lease, resourcesJSON, acquiredAt, expiresAt)
	if err != nil {
		return operation.LockLease{}, false, err
	}
	return lease, true, nil
}

func decodeOperationLease(lease operation.LockLease, resourcesJSON, acquiredAt, expiresAt string) (operation.LockLease, error) {
	if err := json.Unmarshal([]byte(resourcesJSON), &lease.Resources); err != nil {
		return operation.LockLease{}, fmt.Errorf("decode operation lease resources: %w", err)
	}
	var err error
	lease.AcquiredAt, err = parseTime(acquiredAt, "operation lease acquisition")
	if err != nil {
		return operation.LockLease{}, err
	}
	lease.ExpiresAt, err = parseTime(expiresAt, "operation lease expiry")
	if err != nil {
		return operation.LockLease{}, err
	}
	return lease, nil
}

func newOperationLeaseID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate operation lease ID: %w", err)
	}
	return "op-lease-" + hex.EncodeToString(bytes[:]), nil
}

var _ operation.LockManager = (*Store)(nil)
