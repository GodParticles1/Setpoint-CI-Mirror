package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type cleanupStaging struct {
	exists        bool
	dropCalls     int
	recreateCalls int
	dropErr       error
	existsErr     error
	keepAfterDrop bool
}

func (stage *cleanupStaging) Recreate(context.Context, Endpoint, string, string, string) error {
	stage.recreateCalls++
	stage.exists = true
	return nil
}

func (stage *cleanupStaging) Drop(context.Context, Endpoint, string, string) error {
	stage.dropCalls++
	if stage.dropErr == nil && !stage.keepAfterDrop {
		stage.exists = false
	}
	return stage.dropErr
}

func (stage *cleanupStaging) Exists(context.Context, Endpoint, string, string) (bool, error) {
	if stage.existsErr != nil {
		return false, stage.existsErr
	}
	return stage.exists, nil
}

func nativeCleanupEntry(request NativeChunkExecution, state LedgerState) LedgerEntry {
	entry := LedgerEntry{
		Key:          ledgerKeyForChunk(request.Chunk),
		Strategy:     request.Chunk.Strategy,
		State:        state,
		Attempt:      1,
		StagingTable: request.Chunk.StagingTable,
		LastError:    "injected transfer failure",
		UpdatedAt:    time.Now().UTC(),
	}
	if state == LedgerRollbackPending {
		entry.Checkpoint = "staging_drop_pending"
	}
	return entry
}

func TestNativeExecutionFailedStateResumesCleanupWithoutTransferReplay(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerFailed)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "injected transfer failure") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerRolledBack || stage.dropCalls != 1 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("result=%#v drop=%d recreate=%d transfer=%d", result, stage.dropCalls, stage.recreateCalls, transport.calls)
	}
}

func TestNativeExecutionRollbackPendingReconcilesAbsentStagingWithoutDrop(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerRollbackPending)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil {
		t.Fatalf("result=%#v", result)
	}
	if result.Entry.State != LedgerRolledBack || stage.dropCalls != 0 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("result=%#v drop=%d recreate=%d transfer=%d", result, stage.dropCalls, stage.recreateCalls, transport.calls)
	}
}

func TestNativeExecutionCleanupIntentPersistenceFailureBlocksDrop(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	transport := &fakeTransport{}
	store := &failPutLedger{base: ledger, failOnPut: 1}
	engine, request := nativeExecutionFixture(t, store, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerFailed)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist Native staging rollback intent") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.dropCalls != 0 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("drop=%d recreate=%d transfer=%d", stage.dropCalls, stage.recreateCalls, transport.calls)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerFailed {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestNativeExecutionFailureStatePersistenceBlocksCleanup(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	transport := &fakeTransport{firstErr: errors.New("transfer failed")}
	store := &failPutLedger{base: ledger, failOnPut: 4}
	engine, request := nativeExecutionFixture(t, store, stage, transport, &fakeVerifier{source: DataFingerprint{Rows: 1}})

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist failed Native staging state") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.dropCalls != 0 || stage.recreateCalls != 1 || transport.calls != 1 {
		t.Fatalf("drop=%d recreate=%d transfer=%d", stage.dropCalls, stage.recreateCalls, transport.calls)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerStaging {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestNativeExecutionFinalCleanupPersistenceFailureResumesByObservation(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	transport := &fakeTransport{}
	entryStore := &failPutLedger{base: ledger, failOnPut: 1}
	engine, request := nativeExecutionFixture(t, entryStore, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerRollbackPending)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist verified Native staging cleanup") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}

	recovery, fixture := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	result, err = recovery.Execute(context.Background(), fixture)
	if err == nil || result.Entry.State != LedgerRolledBack {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.dropCalls != 0 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("drop=%d recreate=%d transfer=%d", stage.dropCalls, stage.recreateCalls, transport.calls)
	}
}

func TestNativeExecutionCleanupRejectsForeignStagingOwnership(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerFailed)
	foreign, err := BuildStagingTableName("another-run", request.Chunk.TargetTable)
	if err != nil {
		t.Fatal(err)
	}
	entry.StagingTable = foreign
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.dropCalls != 0 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("drop=%d recreate=%d transfer=%d", stage.dropCalls, stage.recreateCalls, transport.calls)
	}
}

func TestNativeExecutionDropFailureStaysRollbackPending(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true, dropErr: errors.New("drop unavailable")}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerFailed)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "drop unavailable") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerRollbackPending || stage.dropCalls != 1 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("result=%#v drop=%d recreate=%d transfer=%d", result, stage.dropCalls, stage.recreateCalls, transport.calls)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestNativeExecutionRollbackFailedPersistenceErrorKeepsPendingEvidence(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true, keepAfterDrop: true}
	transport := &fakeTransport{}
	store := &failPutLedger{base: ledger, failOnPut: 2}
	engine, request := nativeExecutionFixture(t, store, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerFailed)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "still exists") || !strings.Contains(err.Error(), "persist Native staging rollback failure") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}
