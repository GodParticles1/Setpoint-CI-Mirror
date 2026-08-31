package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type authorityStub struct {
	lease        operation.LockLease
	leaseErr     error
	putErr       error
	getErr       error
	listErr      error
	puts         int
	gets         int
	lists        int
	ledgerEntry  clickhouse.LedgerEntry
	ledgerFound  bool
	restorePuts  int
	restoreGets  int
	restoreLists int
}

func (stub *authorityStub) PutRestore(_ context.Context, _ string, _ protocol.OperationActionScope, _ clickhouse.RestoreRecord) error {
	stub.restorePuts++
	return stub.putErr
}
func (stub *authorityStub) GetRestore(_ context.Context, _ string, _ protocol.OperationActionScope, _ clickhouse.RestoreKey) (clickhouse.RestoreRecord, bool, error) {
	stub.restoreGets++
	return clickhouse.RestoreRecord{}, false, stub.getErr
}
func (stub *authorityStub) ListRestores(_ context.Context, _ string, _ protocol.OperationActionScope) ([]clickhouse.RestoreRecord, error) {
	stub.restoreLists++
	return nil, stub.listErr
}

func (stub *authorityStub) ValidateLease(context.Context, string, protocol.OperationActionScope) (operation.LockLease, error) {
	return stub.lease, stub.leaseErr
}
func (stub *authorityStub) PutLedger(_ context.Context, _ string, _ protocol.OperationActionScope, _ clickhouse.LedgerEntry) error {
	stub.puts++
	return stub.putErr
}
func (stub *authorityStub) GetLedger(_ context.Context, _ string, _ protocol.OperationActionScope, _ clickhouse.LedgerKey) (clickhouse.LedgerEntry, bool, error) {
	stub.gets++
	return stub.ledgerEntry, stub.ledgerFound, stub.getErr
}
func (stub *authorityStub) ListLedger(_ context.Context, _ string, _ protocol.OperationActionScope) ([]clickhouse.LedgerEntry, error) {
	stub.lists++
	return nil, stub.listErr
}

func TestRemoteLeaseHandleAlwaysConsultsServerAuthority(t *testing.T) {
	now := time.Now().UTC()
	scope := protocol.OperationActionScope{ClaimID: "claim-1", RunID: "run-1", Action: task.OperationActionApply}
	stub := &authorityStub{lease: operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: "node||node-1||"}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}}
	handle, err := newRemoteLeaseHandle(context.Background(), stub, "task-1", scope)
	if err != nil {
		t.Fatal(err)
	}
	if current := handle.Current(); current.ID != "" {
		t.Fatalf("static lease authority leaked before validation: %#v", current)
	}
	if err := handle.Validate(now); err != nil {
		t.Fatal(err)
	}
	stub.leaseErr = errors.New("server unavailable")
	if err := handle.Validate(now); err == nil {
		t.Fatal("cached lease authorized destructive action after server authority became unavailable")
	}
}

func TestRemoteLedgerStoreRejectsCrossRunAndTransportFailure(t *testing.T) {
	scope := protocol.OperationActionScope{ClaimID: "claim-1", RunID: "run-1", Action: task.OperationActionApply}
	stub := &authorityStub{putErr: errors.New("transport unavailable")}
	store, err := newRemoteLedgerStore(context.Background(), stub, "task-1", scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), clickhouse.LedgerKey{RunID: "run-2"}); err == nil || stub.gets != 0 {
		t.Fatalf("cross-run get reached server: gets=%d err=%v", stub.gets, err)
	}
	if _, err := store.ListRun(context.Background(), "run-2"); err == nil || stub.lists != 0 {
		t.Fatalf("cross-run list reached server: lists=%d err=%v", stub.lists, err)
	}
	if err := store.Put(context.Background(), clickhouse.LedgerEntry{Key: clickhouse.LedgerKey{RunID: "run-1"}}); err == nil || stub.puts != 1 {
		t.Fatalf("transport failure produced optimistic ledger success: puts=%d err=%v", stub.puts, err)
	}
}

func TestRemoteRestoreStoreRejectsCrossRunAndTransportFailure(t *testing.T) {
	scope := protocol.OperationActionScope{ClaimID: "claim-1", RunID: "run-1", Action: task.OperationActionCreateRestorePoint}
	stub := &authorityStub{putErr: errors.New("transport unavailable")}
	store, err := newRemoteRestoreStore(context.Background(), stub, "task-1", scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetRestore(context.Background(), clickhouse.RestoreKey{RunID: "run-2"}); err == nil || stub.restoreGets != 0 {
		t.Fatalf("cross-run restore get reached server: gets=%d err=%v", stub.restoreGets, err)
	}
	if _, err := store.ListRestores(context.Background(), "run-2"); err == nil || stub.restoreLists != 0 {
		t.Fatalf("cross-run restore list reached server: lists=%d err=%v", stub.restoreLists, err)
	}
	if err := store.PutRestore(context.Background(), clickhouse.RestoreRecord{Key: clickhouse.RestoreKey{RunID: "run-1"}}); err == nil || stub.restorePuts != 1 {
		t.Fatalf("transport failure produced optimistic restore success: puts=%d err=%v", stub.restorePuts, err)
	}
}
