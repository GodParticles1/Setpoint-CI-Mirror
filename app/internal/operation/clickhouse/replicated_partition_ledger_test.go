package clickhouse

import "testing"

func TestReplicatedPartitionLedgerRequiresObservationBetweenVerifiedAndCommitted(t *testing.T) {
	if !CanLedgerTransition(LedgerVerified, LedgerCommitPending) {
		t.Fatal("verified -> commit_pending must be allowed for the replicated partition lab")
	}
	if CanLedgerTransition(LedgerCommitPending, LedgerVerified) {
		t.Fatal("commit_pending must never return to verified for blind transfer retry")
	}
	if !CanLedgerTransition(LedgerCommitPending, LedgerReplicasConverging) || !CanLedgerTransition(LedgerReplicasConverging, LedgerCommitted) {
		t.Fatal("replica observation path to committed is incomplete")
	}
	if !CanLedgerTransition(LedgerCommitPending, LedgerCommitUnknown) || !CanLedgerTransition(LedgerCommitUnknown, LedgerReplicasConverging) {
		t.Fatal("ambiguous replicated commit reconciliation transitions are incomplete")
	}
}
