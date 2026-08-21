package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeExecutionBlocksStagingMutationWhenIntentCannotPersist(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	store := &failPutLedger{base: ledger, failOnPut: 1}
	engine, request := nativeExecutionFixture(t, store, stage, &fakeTransport{}, &fakeVerifier{source: DataFingerprint{Rows: 1}})

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist staging recreation intent") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.recreateCalls != 0 || stage.dropCalls != 0 {
		t.Fatalf("staging mutation occurred without durable intent: recreate=%d drop=%d", stage.recreateCalls, stage.dropCalls)
	}
}

func TestNativeExecutionResumesPersistedStagingIntentAfterRestart(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	transport := &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	entry := nativeCleanupEntry(request, LedgerPlanned)
	entry.Checkpoint, entry.Source, entry.LastError = "staging_recreate_intent", fingerprint, ""
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err != nil || result.Entry.State != LedgerVerified || result.Entry.Attempt != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.recreateCalls != 1 || transport.calls != 1 {
		t.Fatalf("recreate=%d transfer=%d", stage.recreateCalls, transport.calls)
	}
}

func TestNativeExecutionBlocksEmptySourceDriftAfterIntent(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: DataFingerprint{Rows: 1}})
	entry := nativeCleanupEntry(request, LedgerPlanned)
	entry.Checkpoint, entry.Source, entry.LastError = "staging_recreate_intent", DataFingerprint{}, ""
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "source fingerprint changed") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("mutation occurred after empty-source drift: recreate=%d transfer=%d", stage.recreateCalls, transport.calls)
	}
}

func TestAtomicExchangeEmptySourceNoopRequiresRuntimeProof(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.Source, entry.Target = DataFingerprint{}, DataFingerprint{}
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	drifted := DataFingerprint{Rows: 1, HashSum64: "1", HashXor64: "1"}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{{}, drifted, {Rows: 0}}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "no-op is blocked") || result.Entry.State != LedgerVerified || client.exchanges != 0 {
		t.Fatalf("result=%#v err=%v exchanges=%d", result, err, client.exchanges)
	}
}

func TestNativeExecutionRecoversTransferredLedgerWriteFailureWithoutTransferReplay(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{}
	transport := &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	store := &failPutLedger{base: ledger, failOnPut: 4}
	engine, request := nativeExecutionFixture(t, store, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})

	if _, err := engine.Execute(context.Background(), request); err == nil {
		t.Fatal("injected post-transfer ledger failure unexpectedly succeeded")
	}
	persisted, ok, err := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if err != nil || !ok || persisted.State != LedgerStaging || persisted.Checkpoint != "native_transfer_intent" {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, err)
	}

	recovery, recoveryRequest := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	result, err := recovery.Execute(context.Background(), recoveryRequest)
	if err != nil || !result.Reconciled || !result.Idempotent || result.Entry.State != LedgerVerified {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if transport.calls != 1 || stage.recreateCalls != 1 {
		t.Fatalf("mutation replayed after physical transfer: transfer=%d recreate=%d", transport.calls, stage.recreateCalls)
	}
}

func TestAtomicExchangeRollbackPendingRestartObservesRestoredStateWithoutReissue(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State, entry.Checkpoint = LedgerRollbackPending, "rollback_exchange_intent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{{Rows: 0}, entry.Source}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := engine.Rollback(context.Background(), request)
	if err != nil || result.Entry.State != LedgerRolledBack || !result.RecoveredAmbiguous {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if client.exchanges != 0 {
		t.Fatalf("rollback mutation was reissued %d time(s)", client.exchanges)
	}
}

func TestAtomicExchangeCommitFinalPersistenceFailureRecoversByObservation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	store := &failPutLedger{base: ledger, failOnPut: 2}
	client := &commitQueryClient{}
	engine, err := NewAtomicExchangeCommitEngine(store, client, &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, entry.Source, {Rows: 0}}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Commit(context.Background(), request); err == nil {
		t.Fatal("injected final commit persistence failure unexpectedly succeeded")
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerCommitUnknown || persisted.Checkpoint != "exchange_intent" {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}

	recovery, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovery.Commit(context.Background(), request)
	if err != nil || result.Entry.State != LedgerCommitted || client.exchanges != 1 {
		t.Fatalf("result=%#v err=%v exchanges=%d", result, err, client.exchanges)
	}
}

func TestAtomicExchangeRollbackPendingRestartProvesMutationNotIssued(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State, entry.Checkpoint = LedgerRollbackPending, "rollback_exchange_intent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "new explicit rollback attempt") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerCommitted || client.exchanges != 0 {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeRollbackFinalPersistenceFailureRecoversByObservation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	store := &failPutLedger{base: ledger, failOnPut: 2}
	client := &commitQueryClient{}
	firstVerifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, {Rows: 0}, entry.Source}}
	engine, err := NewAtomicExchangeCommitEngine(store, client, firstVerifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Rollback(context.Background(), request); err == nil || !strings.Contains(err.Error(), "persist verified rollback state") {
		t.Fatalf("err=%v", err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}

	recovery, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{{Rows: 0}, entry.Source}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovery.Rollback(context.Background(), request)
	if err != nil || result.Entry.State != LedgerRolledBack || client.exchanges != 1 {
		t.Fatalf("result=%#v err=%v exchanges=%d", result, err, client.exchanges)
	}
}

func TestAtomicExchangeRollbackObservationConflictFailsClosed(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State, entry.Checkpoint = LedgerRollbackPending, "rollback_exchange_intent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	conflictTarget := DataFingerprint{Rows: 4, HashSum64: "4", HashXor64: "4"}
	conflictStaging := DataFingerprint{Rows: 6, HashSum64: "6", HashXor64: "6"}
	client := &commitQueryClient{}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{conflictTarget, conflictStaging}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.ReconcileRollback(context.Background(), request)
	if err == nil || result.Entry.State != LedgerRollbackFailed || client.exchanges != 0 {
		t.Fatalf("result=%#v err=%v exchanges=%d", result, err, client.exchanges)
	}
}

func TestAtomicExchangeCommittedTerminalStateRequiresRuntimeProof(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	drifted := DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{drifted, {Rows: 0}}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "do not prove") || result.Entry.State != LedgerCommitted || client.exchanges != 0 {
		t.Fatalf("result=%#v err=%v exchanges=%d", result, err, client.exchanges)
	}
}

func TestAtomicExchangeRolledBackTerminalStateRequiresRuntimeProof(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerRolledBack
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	drifted := DataFingerprint{Rows: 4, HashSum64: "4", HashXor64: "4"}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{drifted, entry.Source}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "do not prove") || result.Entry.State != LedgerRolledBack || client.exchanges != 0 {
		t.Fatalf("result=%#v err=%v exchanges=%d", result, err, client.exchanges)
	}
}

func TestNativeCleanupRejectsPostCommitState(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	engine, request := nativeExecutionFixture(t, ledger, stage, &fakeTransport{}, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerCommitted)
	result, err := engine.reconcileStagingCleanup(context.Background(), entry, request.Pair.Target, request.Chunk, errors.New("cleanup requested"))
	if err == nil || !strings.Contains(err.Error(), "unavailable") || result.Entry.State != LedgerCommitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.dropCalls != 0 {
		t.Fatalf("post-commit staging was dropped %d time(s)", stage.dropCalls)
	}
}

func TestNativeExecutionDoesNotTreatExchangeRollbackIntentAsStagingCleanup(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerRollbackPending)
	entry.Checkpoint = "rollback_exchange_intent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "rollback reconciliation") || result.Entry.State != LedgerRollbackPending {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.dropCalls != 0 || stage.recreateCalls != 0 || transport.calls != 0 {
		t.Fatalf("cross-path mutation occurred: drop=%d recreate=%d transfer=%d", stage.dropCalls, stage.recreateCalls, transport.calls)
	}
}

func TestNativeExecutionVerifiedRestartRequiresRuntimeProof(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	transport := &fakeTransport{}
	recorded := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	drifted := DataFingerprint{Rows: 9, HashSum64: "90", HashXor64: "6"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: recorded, target: drifted})
	entry := nativeCleanupEntry(request, LedgerVerified)
	entry.Source, entry.Target, entry.Checkpoint = recorded, recorded, "native_bytes=128"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "surviving staging fingerprint does not match") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.recreateCalls != 0 || stage.dropCalls != 0 || transport.calls != 0 {
		t.Fatalf("mutation occurred while verified runtime proof failed: recreate=%d drop=%d transfer=%d", stage.recreateCalls, stage.dropCalls, transport.calls)
	}
}

func TestNativeExecutionCommittedStateCannotReenterTransfer(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &cleanupStaging{exists: true}
	transport := &fakeTransport{}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{})
	entry := nativeCleanupEntry(request, LedgerCommitted)
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "transfer execution is blocked") || result.Entry.State != LedgerCommitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if stage.recreateCalls != 0 || stage.dropCalls != 0 || transport.calls != 0 {
		t.Fatalf("committed transfer path mutated state: recreate=%d drop=%d transfer=%d", stage.recreateCalls, stage.dropCalls, transport.calls)
	}
}

func TestReplicatedCommitUnknownPersistenceFailureIsExplicit(t *testing.T) {
	ledger, state, client, _, request := replicatedPartitionLabFixture(t)
	client.replaceMode = "none"
	client.replaceErr = errors.New("timeout after send")
	store := &failPutLedger{base: ledger, failOnPut: 2}
	engine, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist replicated commit_unknown state") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerCommitPending || client.replaces != 1 {
		t.Fatalf("persisted=%#v ok=%t err=%v replaces=%d", persisted, ok, readErr, client.replaces)
	}
}

func TestReplicatedRollbackBlockedPersistenceFailureIsExplicit(t *testing.T) {
	ledger, state, client, _, request := replicatedPartitionLabFixture(t)
	state.setAll(state.source, 1)
	entry, _, _ := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	state.set("r3", DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}, 1)
	store := &failPutLedger{base: ledger, failOnPut: 1}
	engine, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist replicated rollback_blocked state") || client.drops != 0 {
		t.Fatalf("result=%#v err=%v drops=%d", result, err, client.drops)
	}
}

func TestReplicatedRollbackFailurePersistenceKeepsPendingEvidence(t *testing.T) {
	ledger, state, client, _, request := replicatedPartitionLabFixture(t)
	state.setAll(state.source, 1)
	entry, _, _ := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client.dropMode = "conflict"
	store := &failPutLedger{base: ledger, failOnPut: 2}
	engine, err := NewReplicatedPartitionLabCommitEngine(store, client, &replicaLabVerifier{state: state}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist replicated rollback_failed state") || client.drops != 1 {
		t.Fatalf("result=%#v err=%v drops=%d", result, err, client.drops)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), ledgerKeyForChunk(request.Chunk))
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}
