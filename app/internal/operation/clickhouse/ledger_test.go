package clickhouse

import (
	"testing"
	"time"
)

func TestLedgerRequiresVerificationBeforeCommit(t *testing.T) {
	if CanLedgerTransition(LedgerTransferred, LedgerCommitted) { t.Fatal("transferred -> committed must be rejected") }
	if !CanLedgerTransition(LedgerTransferred, LedgerVerified) || !CanLedgerTransition(LedgerVerified, LedgerCommitted) { t.Fatal("verified checkpoint path is required") }
}

func TestValidateLedgerEntry(t *testing.T) {
	entry := LedgerEntry{Key: LedgerKey{RunID: "run-1", Database: "message_center", Table: "alarm", Chunk: 1}, Strategy: StrategyNativeStream, State: LedgerPlanned, Attempt: 1, UpdatedAt: time.Now()}
	if err := ValidateLedgerEntry(entry); err != nil { t.Fatalf("valid ledger entry rejected: %v", err) }
	entry.Key.Chunk = 0
	if err := ValidateLedgerEntry(entry); err == nil { t.Fatal("zero chunk accepted") }
}

func TestValidateLedgerEntryRejectsUnknownStateAndStrategy(t *testing.T) {
	base := LedgerEntry{
		Key: LedgerKey{RunID: "run-1", Database: "message_center", Table: "alarm", Chunk: 1},
		Strategy: StrategyNativeStream,
		State: LedgerPlanned,
		Attempt: 1,
		UpdatedAt: time.Now(),
	}

	unknownState := base
	unknownState.State = LedgerState("future_or_corrupt_state")
	if err := ValidateLedgerEntry(unknownState); err == nil {
		t.Fatal("unknown persisted ledger state accepted")
	}

	unknownStrategy := base
	unknownStrategy.Strategy = StrategyID("future_or_corrupt_strategy")
	if err := ValidateLedgerEntry(unknownStrategy); err == nil {
		t.Fatal("unknown persisted ledger strategy accepted")
	}
}
