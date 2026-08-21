package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type allowCommitGuard struct{}

func (allowCommitGuard) Verify(context.Context, CommitGuardRequest) error { return nil }

type commitQueryClient struct {
	exchangeErr error
	exchanges   int
}

func (client *commitQueryClient) Query(_ context.Context, request QueryRequest) (string, error) {
	if strings.Contains(request.Query, "FROM system.databases") {
		return "Atomic", nil
	}
	if strings.Contains(request.Query, "FROM system.tables") {
		return "MergeTree", nil
	}
	if strings.HasPrefix(request.Query, "EXPLAIN SYNTAX EXCHANGE TABLES") {
		return strings.TrimPrefix(request.Query, "EXPLAIN SYNTAX "), nil
	}
	if strings.HasPrefix(request.Query, "EXCHANGE TABLES") {
		client.exchanges++
		if client.exchangeErr != nil {
			err := client.exchangeErr
			client.exchangeErr = nil
			return "", err
		}
		return "", nil
	}
	return "", nil
}

type sequenceVerifier struct {
	values []DataFingerprint
	index  int
}

func (verifier *sequenceVerifier) Fingerprint(context.Context, Endpoint, string, Table, *TimeRangeFilter) (DataFingerprint, error) {
	if verifier.index >= len(verifier.values) {
		return DataFingerprint{}, errors.New("unexpected fingerprint call")
	}
	value := verifier.values[verifier.index]
	verifier.index++
	return value, nil
}

func verifiedLedger(t *testing.T) (*memoryLedger, LedgerEntry, ExchangeCommitRequest) {
	t.Helper()
	ledger := newMemoryLedger()
	source := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	staging, err := BuildStagingTableName("run-1", "events")
	if err != nil {
		t.Fatal(err)
	}
	entry := LedgerEntry{Key: LedgerKey{RunID: "run-1", Database: "db", Table: "events", Chunk: 1}, Strategy: StrategyNativeStream, State: LedgerVerified, Attempt: 1, StagingTable: staging, Source: source, Target: source, UpdatedAt: time.Now()}
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	request := ExchangeCommitRequest{Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}, Chunk: TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: staging, Sequence: 1}, TargetTable: Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}}}
	return ledger, entry, request
}

func TestAtomicExchangeCommitVerifiesAndCommits(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	client := &commitQueryClient{}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, entry.Source, {Rows: 0}}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerCommitted || !result.Verification.Passed || !result.RollbackAvailable || client.exchanges != 1 {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeCommitRecoversAmbiguousSuccessByObservation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	client := &commitQueryClient{exchangeErr: errors.New("connection reset after send")}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, entry.Source, {Rows: 0}}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerCommitted || !result.RecoveredAmbiguous {
		t.Fatalf("result=%#v", result)
	}
}

func TestAtomicExchangeCommitMarksUnknownWhenStateCannotBeProven(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	client := &commitQueryClient{exchangeErr: errors.New("timeout")}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, entry.Source, {Rows: 4, HashSum64: "4", HashXor64: "4"}, {Rows: 6, HashSum64: "6", HashXor64: "6"}}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil {
		t.Fatal("ambiguous commit unexpectedly succeeded")
	}
	if result.Entry.State != LedgerCommitUnknown || entry.State != LedgerVerified {
		t.Fatalf("result=%#v", result)
	}
}

func TestAtomicExchangeCommitBlocksStagingFingerprintDriftBeforeMutation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	client := &commitQueryClient{}
	drifted := DataFingerprint{Rows: 9, HashSum64: "90", HashXor64: "6"}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, drifted}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "fingerprint changed") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerVerified || client.exchanges != 0 || entry.State != LedgerVerified {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeCommitBlocksSourceFingerprintDriftBeforeMutation(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	client := &commitQueryClient{}
	drifted := DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, &sequenceVerifier{values: []DataFingerprint{drifted}}, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Commit(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "source fingerprint changed") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Entry.State != LedgerVerified || client.exchanges != 0 || entry.State != LedgerVerified {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeRollbackRestoresEmptyTargetAndPreservesMigratedStaging(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	verifier := &sequenceVerifier{values: []DataFingerprint{entry.Source, {Rows: 0}, {Rows: 0}, entry.Source}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != LedgerRolledBack || client.exchanges != 1 {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeRollbackBlocksWhenCommittedTargetChanged(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{}
	changed := DataFingerprint{Rows: 11, HashSum64: "101", HashXor64: "8"}
	verifier := &sequenceVerifier{values: []DataFingerprint{changed, {Rows: 0}}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil {
		t.Fatal("changed committed target was automatically rolled back")
	}
	if result.Entry.State != LedgerRollbackBlocked || client.exchanges != 0 {
		t.Fatalf("result=%#v exchanges=%d", result, client.exchanges)
	}
}

func TestAtomicExchangeCommitAndRollbackPreserveNonEmptyBaseline(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	baseline := DataFingerprint{Rows: 4, HashSum64: "40", HashXor64: "4"}
	request.TargetBaseline = &baseline
	client := &commitQueryClient{}
	commitVerifier := &sequenceVerifier{values: []DataFingerprint{
		entry.Source,
		baseline, entry.Source,
		entry.Source, baseline,
	}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, commitVerifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := engine.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Entry.State != LedgerCommitted || !committed.Verification.Passed || client.exchanges != 1 {
		t.Fatalf("committed=%#v exchanges=%d", committed, client.exchanges)
	}

	rollbackVerifier := &sequenceVerifier{values: []DataFingerprint{
		entry.Source, baseline,
		baseline, entry.Source,
	}}
	engine, err = NewAtomicExchangeCommitEngine(ledger, client, rollbackVerifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := engine.Rollback(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Entry.State != LedgerRolledBack || !rolledBack.Verification.Passed || rolledBack.Entry.Target != baseline || client.exchanges != 2 {
		t.Fatalf("rolled_back=%#v exchanges=%d", rolledBack, client.exchanges)
	}
}

func TestAtomicExchangeRollbackUnknownResultKeepsRecoverableLedgerState(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	baseline := DataFingerprint{Rows: 4, HashSum64: "40", HashXor64: "4"}
	request.TargetBaseline = &baseline
	entry.State = LedgerCommitted
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	client := &commitQueryClient{exchangeErr: errors.New("connection lost after rollback send")}
	unknownTarget := DataFingerprint{Rows: 7, HashSum64: "70", HashXor64: "7"}
	unknownStaging := DataFingerprint{Rows: 8, HashSum64: "80", HashXor64: "8"}
	verifier := &sequenceVerifier{values: []DataFingerprint{
		entry.Source, baseline,
		unknownTarget, unknownStaging,
	}}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Rollback(context.Background(), request)
	if err == nil {
		t.Fatal("ambiguous rollback unexpectedly passed")
	}
	if result.Entry.State != LedgerRollbackFailed || result.Entry.LastError == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
