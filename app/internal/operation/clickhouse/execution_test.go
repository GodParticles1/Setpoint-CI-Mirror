package clickhouse

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryLedger struct { mu sync.Mutex; entries map[LedgerKey]LedgerEntry }
func newMemoryLedger() *memoryLedger { return &memoryLedger{entries: map[LedgerKey]LedgerEntry{}} }
func (store *memoryLedger) Put(_ context.Context, entry LedgerEntry) error { store.mu.Lock(); defer store.mu.Unlock(); store.entries[entry.Key] = entry; return nil }
func (store *memoryLedger) Get(_ context.Context, key LedgerKey) (LedgerEntry, bool, error) { store.mu.Lock(); defer store.mu.Unlock(); entry, ok := store.entries[key]; return entry, ok, nil }
func (store *memoryLedger) ListRun(_ context.Context, runID string) ([]LedgerEntry, error) { store.mu.Lock(); defer store.mu.Unlock(); var result []LedgerEntry; for _, entry := range store.entries { if entry.Key.RunID == runID { result = append(result, entry) } }; return result, nil }

type fakeStaging struct { recreate int; drop int; exists bool; existsErr error }
func (stage *fakeStaging) Recreate(context.Context, Endpoint, string, string, string) error { stage.recreate++; stage.exists = true; return nil }
func (stage *fakeStaging) Drop(context.Context, Endpoint, string, string) error { stage.drop++; stage.exists = false; return nil }
func (stage *fakeStaging) Exists(context.Context, Endpoint, string, string) (bool, error) { return stage.exists, stage.existsErr }

type fakeTransport struct { calls int; firstErr error }
func (transport *fakeTransport) Transfer(context.Context, NativeTransferRequest) (NativeTransferResult, error) { transport.calls++; if transport.firstErr != nil { err := transport.firstErr; transport.firstErr = nil; return NativeTransferResult{}, err }; return NativeTransferResult{BytesTransferred: 128}, nil }

type fakeVerifier struct { source DataFingerprint; target DataFingerprint; calls int }
func (verifier *fakeVerifier) Fingerprint(_ context.Context, endpoint Endpoint, _ string, _ Table, filter *TimeRangeFilter) (DataFingerprint, error) { verifier.calls++; if endpoint.Host == "source" || filter != nil { return verifier.source, nil }; return verifier.target, nil }

func nativeExecutionFixture(t *testing.T, ledger LedgerStore, staging StagingController, transport NativeTransport, verifier FingerprintVerifier) (*NativeExecutionEngine, NativeChunkExecution) {
	t.Helper()
	engine, err := NewNativeExecutionEngine(ledger, staging, transport, verifier)
	if err != nil { t.Fatal(err) }
	stageName, err := BuildStagingTableName("run-1", "events")
	if err != nil { t.Fatal(err) }
	pair := PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}
	table := Table{Database: "db", Name: "events", Columns: []Column{{Name: "id", Position: 1, Type: "UInt64"}}}
	request := NativeChunkExecution{Pair: pair, Chunk: TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: stageName, Sequence: 1}, SourceTable: table, TargetTable: table}
	return engine, request
}

func TestNativeExecutionTransfersVerifiesAndIsIdempotent(t *testing.T) {
	ledger, stage, transport := newMemoryLedger(), &fakeStaging{}, &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	verifier := &fakeVerifier{source: fingerprint, target: fingerprint}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, verifier)
	result, err := engine.Execute(context.Background(), request)
	if err != nil { t.Fatal(err) }
	if result.Entry.State != LedgerVerified || !result.Verification.Passed || !result.Transferred { t.Fatalf("result=%#v", result) }
	second, err := engine.Execute(context.Background(), request)
	if err != nil { t.Fatal(err) }
	if !second.Idempotent || transport.calls != 1 || stage.recreate != 1 { t.Fatalf("second=%#v calls=%d recreate=%d", second, transport.calls, stage.recreate) }
}

func TestNativeExecutionResumesInterruptedTransferFromMissingStaging(t *testing.T) {
	ledger, stage := newMemoryLedger(), &fakeStaging{}
	transport := &fakeTransport{firstErr: context.Canceled}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	_, _ = engine.Execute(ctx, request)
	entry, ok, err := ledger.Get(context.Background(), LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1})
	if err != nil || !ok { t.Fatalf("ledger missing: %v", err) }
	if entry.State != LedgerStaging { t.Fatalf("state=%s", entry.State) }
	stage.exists = false
	result, err := engine.Execute(context.Background(), request)
	if err != nil { t.Fatal(err) }
	if result.Entry.State != LedgerVerified || result.Entry.Attempt != 2 || stage.recreate != 2 || transport.calls != 2 { t.Fatalf("result=%#v recreate=%d transport=%d", result, stage.recreate, transport.calls) }
}

func TestNativeExecutionReusesPhysicallyCompletedStagingAfterLedgerCrash(t *testing.T) {
	ledger, stage, transport := newMemoryLedger(), &fakeStaging{exists: true}, &fakeTransport{}
	fingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: fingerprint, target: fingerprint})
	entry := LedgerEntry{
		Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1},
		Strategy: StrategyNativeStream, State: LedgerStaging, Attempt: 1, StagingTable: request.Chunk.StagingTable,
		Source: fingerprint, UpdatedAt: time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil { t.Fatal(err) }
	result, err := engine.Execute(context.Background(), request)
	if err != nil { t.Fatal(err) }
	if !result.Reconciled || !result.Idempotent || result.Entry.State != LedgerVerified || !result.Verification.Passed {
		t.Fatalf("result=%#v", result)
	}
	if transport.calls != 0 || stage.recreate != 0 {
		t.Fatalf("blind replay occurred: transport=%d recreate=%d", transport.calls, stage.recreate)
	}
}

func TestNativeExecutionBlocksTransferredLedgerWhenStagingFingerprintDiffers(t *testing.T) {
	ledger, stage, transport := newMemoryLedger(), &fakeStaging{exists: true}, &fakeTransport{}
	source := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	target := DataFingerprint{Rows: 9, HashSum64: "90", HashXor64: "6"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: source, target: target})
	entry := LedgerEntry{
		Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1},
		Strategy: StrategyNativeStream, State: LedgerTransferred, Attempt: 1, StagingTable: request.Chunk.StagingTable,
		Source: source, UpdatedAt: time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil { t.Fatal(err) }
	if _, err := engine.Execute(context.Background(), request); err == nil { t.Fatal("mismatched transferred staging unexpectedly replayed") }
	if transport.calls != 0 || stage.recreate != 0 { t.Fatalf("unsafe retry: transport=%d recreate=%d", transport.calls, stage.recreate) }
}

func TestNativeExecutionBlocksReconcileWhenSourceChanged(t *testing.T) {
	ledger, stage, transport := newMemoryLedger(), &fakeStaging{exists: true}, &fakeTransport{}
	oldSource := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	newSource := DataFingerprint{Rows: 11, HashSum64: "111", HashXor64: "8"}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, &fakeVerifier{source: newSource, target: oldSource})
	entry := LedgerEntry{
		Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1},
		Strategy: StrategyNativeStream, State: LedgerStaging, Attempt: 1, StagingTable: request.Chunk.StagingTable,
		Source: oldSource, UpdatedAt: time.Now().UTC(),
	}
	if err := ledger.Put(context.Background(), entry); err != nil { t.Fatal(err) }
	if _, err := engine.Execute(context.Background(), request); err == nil { t.Fatal("source drift unexpectedly replayed") }
	if transport.calls != 0 || stage.recreate != 0 { t.Fatalf("unsafe retry: transport=%d recreate=%d", transport.calls, stage.recreate) }
}

func TestNativeExecutionRollsBackRunOwnedStagingOnVerificationMismatch(t *testing.T) {
	ledger, stage, transport := newMemoryLedger(), &fakeStaging{}, &fakeTransport{}
	verifier := &fakeVerifier{source: DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}, target: DataFingerprint{Rows: 9, HashSum64: "99", HashXor64: "6"}}
	engine, request := nativeExecutionFixture(t, ledger, stage, transport, verifier)
	result, err := engine.Execute(context.Background(), request)
	if err == nil { t.Fatal("verification mismatch unexpectedly succeeded") }
	if result.Entry.State != LedgerRolledBack || stage.drop != 1 { t.Fatalf("result=%#v drop=%d", result, stage.drop) }
}

func TestNativeExecutionRollsBackOnTransportFailure(t *testing.T) {
	ledger, stage := newMemoryLedger(), &fakeStaging{}
	engine, request := nativeExecutionFixture(t, ledger, stage, &fakeTransport{firstErr: errors.New("network failure")}, &fakeVerifier{source: DataFingerprint{Rows: 1}, target: DataFingerprint{Rows: 1}})
	result, err := engine.Execute(context.Background(), request)
	if err == nil { t.Fatal("transport failure unexpectedly succeeded") }
	if result.Entry.State != LedgerRolledBack || stage.drop != 1 { t.Fatalf("result=%#v drop=%d", result, stage.drop) }
}
