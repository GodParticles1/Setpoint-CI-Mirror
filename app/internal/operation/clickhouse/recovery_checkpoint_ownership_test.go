package clickhouse

import (
	"strings"
	"testing"
	"time"
)

func recoveryOwnershipFixture(t *testing.T, partition string) (LedgerEntry, TransferChunk) {
	t.Helper()
	staging, err := BuildStagingTableName("run-ownership", "events")
	if err != nil {
		t.Fatal(err)
	}
	chunk := TransferChunk{
		RunID: "run-ownership", Strategy: StrategyNativeStream,
		SourceDatabase: "db", SourceTable: "events",
		TargetDatabase: "db", TargetTable: "events",
		StagingTable: staging, Partition: partition, Sequence: 1,
	}
	entry := LedgerEntry{
		Key: ledgerKeyForChunk(chunk), Strategy: chunk.Strategy,
		Attempt: 1, StagingTable: staging, UpdatedAt: time.Now().UTC(),
	}
	return entry, chunk
}

func TestRecoveryCheckpointOwnershipKeepsCommitAdaptersSeparated(t *testing.T) {
	wholeEntry, wholeChunk := recoveryOwnershipFixture(t, "")
	wholeEntry.State, wholeEntry.Checkpoint = LedgerCommitUnknown, "exchange_intent"
	if err := validateLedgerOwnership(wholeEntry, wholeChunk); err != nil {
		t.Fatalf("valid Atomic EXCHANGE recovery rejected: %v", err)
	}
	wholeEntry.Checkpoint = "commit_unknown"
	if err := validateLedgerOwnership(wholeEntry, wholeChunk); err == nil || !strings.Contains(err.Error(), "Atomic EXCHANGE") {
		t.Fatalf("replicated commit checkpoint accepted by whole-table path: %v", err)
	}

	partitionEntry, partitionChunk := recoveryOwnershipFixture(t, "202608")
	partitionEntry.State, partitionEntry.Checkpoint = LedgerCommitUnknown, "commit_unknown"
	if err := validateLedgerOwnership(partitionEntry, partitionChunk); err != nil {
		t.Fatalf("valid replicated partition recovery rejected: %v", err)
	}
	partitionEntry.Checkpoint = "exchange_intent"
	if err := validateLedgerOwnership(partitionEntry, partitionChunk); err == nil || !strings.Contains(err.Error(), "replicated partition") {
		t.Fatalf("Atomic EXCHANGE checkpoint accepted by partition path: %v", err)
	}
}

func TestRecoveryCheckpointOwnershipKeepsRollbackAdaptersSeparated(t *testing.T) {
	wholeEntry, wholeChunk := recoveryOwnershipFixture(t, "")
	wholeEntry.State, wholeEntry.Checkpoint = LedgerRollbackPending, "rollback_exchange_intent"
	if err := validateLedgerOwnership(wholeEntry, wholeChunk); err != nil {
		t.Fatalf("valid Atomic rollback recovery rejected: %v", err)
	}
	wholeEntry.Checkpoint = "drop_partition_pending"
	if err := validateLedgerOwnership(wholeEntry, wholeChunk); err == nil || !strings.Contains(err.Error(), "Atomic EXCHANGE") {
		t.Fatalf("replicated rollback checkpoint accepted by whole-table path: %v", err)
	}

	partitionEntry, partitionChunk := recoveryOwnershipFixture(t, "202608")
	partitionEntry.State, partitionEntry.Checkpoint = LedgerRollbackPending, "drop_partition_pending"
	if err := validateLedgerOwnership(partitionEntry, partitionChunk); err != nil {
		t.Fatalf("valid replicated rollback recovery rejected: %v", err)
	}
	partitionEntry.Checkpoint = "rollback_exchange_intent"
	if err := validateLedgerOwnership(partitionEntry, partitionChunk); err == nil || !strings.Contains(err.Error(), "replicated partition") {
		t.Fatalf("Atomic rollback checkpoint accepted by partition path: %v", err)
	}
}

func TestRecoveryCheckpointOwnershipAllowsNativePrecommitCleanupInBothScopes(t *testing.T) {
	for _, partition := range []string{"", "202608"} {
		entry, chunk := recoveryOwnershipFixture(t, partition)
		entry.State, entry.Checkpoint = LedgerRollbackPending, "staging_drop_pending"
		if err := validateLedgerOwnership(entry, chunk); err != nil {
			t.Fatalf("partition=%q Native staging cleanup ownership rejected: %v", partition, err)
		}
	}
}
