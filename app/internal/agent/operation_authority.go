package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type OperationLeaseAuthority interface {
	ValidateLease(context.Context, string, protocol.OperationActionScope) (operation.LockLease, error)
}

type ClickHouseOperationAuthority interface {
	OperationLeaseAuthority
	PutLedger(context.Context, string, protocol.OperationActionScope, clickhouse.LedgerEntry) error
	GetLedger(context.Context, string, protocol.OperationActionScope, clickhouse.LedgerKey) (clickhouse.LedgerEntry, bool, error)
	ListLedger(context.Context, string, protocol.OperationActionScope) ([]clickhouse.LedgerEntry, error)
	PutRestore(context.Context, string, protocol.OperationActionScope, clickhouse.RestoreRecord) error
	GetRestore(context.Context, string, protocol.OperationActionScope, clickhouse.RestoreKey) (clickhouse.RestoreRecord, bool, error)
	ListRestores(context.Context, string, protocol.OperationActionScope) ([]clickhouse.RestoreRecord, error)
}

type remoteLeaseHandle struct {
	ctx       context.Context
	authority OperationLeaseAuthority
	taskID    string
	scope     protocol.OperationActionScope
	mu        sync.RWMutex
	current   operation.LockLease
}

func newRemoteLeaseHandle(ctx context.Context, authority OperationLeaseAuthority, taskID string, scope protocol.OperationActionScope) (*remoteLeaseHandle, error) {
	if authority == nil || taskID == "" {
		return nil, errors.New("server-authoritative lease adapter is required")
	}
	return &remoteLeaseHandle{ctx: ctx, authority: authority, taskID: taskID, scope: scope}, nil
}

func (handle *remoteLeaseHandle) Current() operation.LockLease {
	handle.mu.RLock()
	defer handle.mu.RUnlock()
	return handle.current
}

func (handle *remoteLeaseHandle) Validate(now time.Time) error {
	lease, err := handle.authority.ValidateLease(handle.ctx, handle.taskID, handle.scope)
	if err != nil {
		return fmt.Errorf("validate current server-authoritative operation lease: %w", err)
	}
	if err := operation.ValidateLeaseCoverage(lease, handle.scope.RunID, lease.Resources, now); err != nil {
		return fmt.Errorf("validate returned server-authoritative operation lease: %w", err)
	}
	handle.mu.Lock()
	handle.current = lease
	handle.mu.Unlock()
	return nil
}

type remoteLedgerStore struct {
	ctx       context.Context
	authority ClickHouseOperationAuthority
	taskID    string
	scope     protocol.OperationActionScope
}

func newRemoteLedgerStore(ctx context.Context, authority ClickHouseOperationAuthority, taskID string, scope protocol.OperationActionScope) (*remoteLedgerStore, error) {
	if authority == nil || taskID == "" || scope.RunID == "" {
		return nil, errors.New("server-authoritative ClickHouse ledger adapter is required")
	}
	return &remoteLedgerStore{ctx: ctx, authority: authority, taskID: taskID, scope: scope}, nil
}

func (store *remoteLedgerStore) Put(ctx context.Context, entry clickhouse.LedgerEntry) error {
	if entry.Key.RunID != store.scope.RunID {
		return errors.New("ledger entry belongs to a different operation run")
	}
	return store.authority.PutLedger(ctx, store.taskID, store.scope, entry)
}

func (store *remoteLedgerStore) Get(ctx context.Context, key clickhouse.LedgerKey) (clickhouse.LedgerEntry, bool, error) {
	if key.RunID != store.scope.RunID {
		return clickhouse.LedgerEntry{}, false, errors.New("ledger key belongs to a different operation run")
	}
	return store.authority.GetLedger(ctx, store.taskID, store.scope, key)
}

func (store *remoteLedgerStore) ListRun(ctx context.Context, runID string) ([]clickhouse.LedgerEntry, error) {
	if runID != store.scope.RunID {
		return nil, errors.New("cross-run ClickHouse ledger access is forbidden")
	}
	return store.authority.ListLedger(ctx, store.taskID, store.scope)
}

type remoteRestoreStore struct {
	ctx       context.Context
	authority ClickHouseOperationAuthority
	taskID    string
	scope     protocol.OperationActionScope
}

func newRemoteRestoreStore(ctx context.Context, authority ClickHouseOperationAuthority, taskID string, scope protocol.OperationActionScope) (*remoteRestoreStore, error) {
	if authority == nil || taskID == "" || scope.RunID == "" {
		return nil, errors.New("server-authoritative ClickHouse restore adapter is required")
	}
	return &remoteRestoreStore{ctx: ctx, authority: authority, taskID: taskID, scope: scope}, nil
}

func (store *remoteRestoreStore) PutRestore(ctx context.Context, record clickhouse.RestoreRecord) error {
	if record.Key.RunID != store.scope.RunID {
		return errors.New("restore record belongs to a different operation run")
	}
	return store.authority.PutRestore(ctx, store.taskID, store.scope, record)
}

func (store *remoteRestoreStore) GetRestore(ctx context.Context, key clickhouse.RestoreKey) (clickhouse.RestoreRecord, bool, error) {
	if key.RunID != store.scope.RunID {
		return clickhouse.RestoreRecord{}, false, errors.New("restore key belongs to a different operation run")
	}
	return store.authority.GetRestore(ctx, store.taskID, store.scope, key)
}

func (store *remoteRestoreStore) ListRestores(ctx context.Context, runID string) ([]clickhouse.RestoreRecord, error) {
	if runID != store.scope.RunID {
		return nil, errors.New("cross-run ClickHouse restore access is forbidden")
	}
	return store.authority.ListRestores(ctx, store.taskID, store.scope)
}

func operationActionScope(resource task.Resource) (protocol.OperationActionScope, error) {
	if resource.Spec.OperationExecution == nil {
		return protocol.OperationActionScope{}, errors.New("operation execution contract is required")
	}
	return protocol.OperationActionScope{
		ClaimID: resource.Status.ClaimID,
		RunID:   resource.Spec.OperationExecution.RunID,
		Action:  resource.Spec.OperationExecution.Action,
	}, nil
}

var _ operation.LeaseHandle = (*remoteLeaseHandle)(nil)
var _ clickhouse.LedgerStore = (*remoteLedgerStore)(nil)
var _ clickhouse.RestoreStore = (*remoteRestoreStore)(nil)
