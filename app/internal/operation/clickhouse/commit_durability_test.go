package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type failPutLedger struct {
	base      *memoryLedger
	putCalls  int
	failOnPut int
}

func (store *failPutLedger) Put(ctx context.Context, entry LedgerEntry) error {
	store.putCalls++
	if store.putCalls == store.failOnPut {
		return errors.New("injected ledger write failure")
	}
	return store.base.Put(ctx, entry)
}

func (store *failPutLedger) Get(ctx context.Context, key LedgerKey) (LedgerEntry, bool, error) {
	return store.base.Get(ctx, key)
}

func (store *failPutLedger) ListRun(ctx context.Context, runID string) ([]LedgerEntry, error) {
	return store.base.ListRun(ctx, runID)
}

type intentCheckingClient struct {
	commitQueryClient
	ledger   *memoryLedger
	key      LedgerKey
	observed bool
}

func (client *intentCheckingClient) Query(ctx context.Context, request QueryRequest) (string, error) {
	if strings.HasPrefix(request.Query, "EXCHANGE TABLES") {
		entry, ok, err := client.ledger.Get(ctx, client.key)
		if err != nil {
			return "", err
		}
		if !ok || entry.State != LedgerCommitUnknown || entry.Checkpoint != "exchange_intent" {
			return "", errors.New("EXCHANGE intent was not durably recorded before mutation")
		}
		client.observed = true
	}
	return client.commitQueryClient.Query(ctx, request)
}

func TestAtomicExchangeCommitPersistsIntentBeforeMutation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	client := &intentCheckingClient{ledger: ledger, key: entry.Key}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, entry.Source, {Rows: 0}}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !client.observed || result.Entry.State != LedgerCommitted {
		t.Fatalf("observed=%t result=%#v", client.observed, result)
	}
}

func TestAtomicExchangeCommitReconcilesPersistedCommittedIntentWithoutReissuingMutation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State, entry.Checkpoint = LedgerCommitUnknown, "exchange_intent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerCommitted || !result.RecoveredAmbiguous || client.exchanges != 0 {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeCommitReconcilesPersistedUncommittedIntentWithoutReissuingMutation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State, entry.Checkpoint = LedgerCommitUnknown, "exchange_intent"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	verifier := &sequenceVerifier{values: []DataFingerprint{{Rows: 0}, entry.Source}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "new explicit execution attempt") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerVerified || !result.RecoveredAmbiguous || client.exchanges != 0 {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeCommitBlocksMutationWhenIntentCannotPersist(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	store := &failPutLedger{base: ledger, failOnPut: 1}
	client := &commitQueryClient{}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source}}
	engine, err := NewAtomicExchangeCommitEngine(store, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist EXCHANGE intent") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if client.exchanges != 0 {
		t.Fatalf("EXCHANGE calls=%d", client.exchanges)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerVerified {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestAtomicExchangeCommitReportsCommitUnknownPersistenceFailure(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	store := &failPutLedger{base: ledger, failOnPut: 2}
	client := &commitQueryClient{exchangeErr: errors.New("timeout")}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, {Rows: 4, HashSum64: "4", HashXor64: "4"}, {Rows: 6, HashSum64: "6", HashXor64: "6"}}}
	engine, err := NewAtomicExchangeCommitEngine(store, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "persist commit_unknown state") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerCommitUnknown || persisted.Checkpoint != "exchange_intent" {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestAtomicExchangeRollbackReportsBlockedStatePersistenceFailure(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	store := &failPutLedger{base: ledger, failOnPut: 1}
	changed := DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}
	engine, err := NewAtomicExchangeCommitEngine(store, &commitQueryClient{}, &sequenceVerifier{values: []DataFingerprint{changed, {Rows: 0}}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "committed target changed") || !strings.Contains(err.Error(), "persist rollback_blocked state") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerCommitted {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}

func TestAtomicExchangeRollbackReportsObservedUnappliedPersistenceFailure(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	store := &failPutLedger{base: ledger, failOnPut: 2}
	client := &commitQueryClient{exchangeErr: errors.New("rollback exchange failed")}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, {Rows: 0}}}
	engine, err := NewAtomicExchangeCommitEngine(store, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "persist observed unapplied rollback state") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	persisted, ok, readErr := ledger.Get(context.Background(), entry.Key)
	if readErr != nil || !ok || persisted.State != LedgerRollbackPending {
		t.Fatalf("persisted=%#v ok=%t err=%v", persisted, ok, readErr)
	}
}
