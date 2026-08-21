package clickhouse

import (
	"context"
	"strings"
	"testing"
)

func TestAtomicExchangeRequestRejectsPartitionAndTimeScope(t *testing.T) {
	_, _, base := verifiedLedger(t)

	partitioned := base
	partitioned.Chunk.Partition = "202608"
	if err := validateExchangeCommitRequest(partitioned); err == nil || !strings.Contains(err.Error(), "whole-table") {
		t.Fatalf("partition-scoped Atomic EXCHANGE accepted: %v", err)
	}

	filtered := base
	filtered.Chunk.Filter = &TimeRangeFilter{Column: "event_time", Start: "2026-08-01T00:00:00Z", End: "2026-08-02T00:00:00Z"}
	if err := validateExchangeCommitRequest(filtered); err == nil || !strings.Contains(err.Error(), "whole-table") {
		t.Fatalf("time-filtered Atomic EXCHANGE accepted: %v", err)
	}
}

func TestAtomicExchangeCannotTakeOverReplicatedCommitRecovery(t *testing.T) {
	ledger, entry, request := verifiedLedger(t)
	request.Chunk.Partition = "202608"
	request.Chunk.StagingTable, _ = BuildStagingTableName(request.Chunk.RunID, request.Chunk.TargetTable)
	entry.Key = ledgerKeyForChunk(request.Chunk)
	entry.State, entry.Checkpoint = LedgerCommitUnknown, "commit_unknown"
	if err := ledger.Put(context.Background(), entry); err != nil {
		t.Fatal(err)
	}

	client := &commitQueryClient{}
	verifier := &sequenceVerifier{}
	engine, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Reconcile(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "whole-table") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if client.exchanges != 0 || verifier.index != 0 {
		t.Fatalf("Atomic adapter performed I/O for replicated recovery: exchanges=%d verifier_calls=%d", client.exchanges, verifier.index)
	}
}
