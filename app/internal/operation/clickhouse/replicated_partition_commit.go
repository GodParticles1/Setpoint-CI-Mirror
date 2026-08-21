package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReplicatedPartitionLabCommitRequest struct {
	Pair           PairParameters
	Chunk          TransferChunk
	TargetTable    Table
	TargetSnapshot Snapshot
}

type ReplicatedPartitionLabCommitResult struct {
	Entry              LedgerEntry            `json:"entry"`
	Replicas           ReplicaPartitionReport `json:"replicas"`
	RollbackAvailable  bool                   `json:"rollback_available"`
	RecoveredAmbiguous bool                   `json:"recovered_ambiguous"`
}

// ReplicatedPartitionLabCommitEngine is deliberately not wired into the product
// OperationDefinition. It exists only for isolated physical lab validation.
type ReplicatedPartitionLabCommitEngine struct {
	ledger    LedgerStore
	client    QueryClient
	partition PartitionFingerprintVerifier
	observer  *ReplicaPartitionObserver
	guard     CommitGuard
	now       func() time.Time
}

func NewReplicatedPartitionLabCommitEngine(ledger LedgerStore, client QueryClient, verifier FingerprintVerifier, guard CommitGuard) (*ReplicatedPartitionLabCommitEngine, error) {
	if ledger == nil || client == nil || verifier == nil || guard == nil {
		return nil, errors.New("ledger, query client, fingerprint verifier and commit guard are required")
	}
	partitionVerifier, ok := verifier.(PartitionFingerprintVerifier)
	if !ok {
		return nil, errors.New("ReplicatedMergeTree lab commit requires a partition-aware fingerprint verifier")
	}
	observer, err := NewReplicaPartitionObserver(client, verifier)
	if err != nil {
		return nil, err
	}
	return &ReplicatedPartitionLabCommitEngine{
		ledger: ledger, client: client, partition: partitionVerifier, observer: observer, guard: guard,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (engine *ReplicatedPartitionLabCommitEngine) Commit(ctx context.Context, request ReplicatedPartitionLabCommitRequest) (ReplicatedPartitionLabCommitResult, error) {
	request, err := normalizeReplicatedPartitionLabRequest(request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	switch entry.State {
	case LedgerCommitted:
		return engine.reconcileCommitted(ctx, request, entry)
	case LedgerCommitPending, LedgerCommitUnknown, LedgerReplicasConverging:
		return engine.ReconcileCommit(ctx, request)
	case LedgerVerified:
	default:
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("replicated partition commit requires verified state, got %s", entry.State)
	}
	if entry.Source.Rows == 0 {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, errors.New("replicated partition lab commit does not write an empty source partition")
	}
	if err := engine.verifyGuard(ctx, request); err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	capability, err := InspectReplicatedPartitionCapability(ctx, engine.client, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	if !capability.ReadyForLab {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("ReplicatedMergeTree partition lab capability is not ready: %s", capability.Reason)
	}
	staging := request.TargetTable
	staging.Name = request.Chunk.StagingTable
	stagingFingerprint, err := engine.partition.FingerprintPartition(ctx, request.Pair.Target, request.Chunk.TargetDatabase, staging, request.Chunk.Partition)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("fingerprint replicated partition staging before commit: %w", err)
	}
	if !CompareFingerprints(entry.Source, stagingFingerprint).Passed {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, errors.New("staging partition no longer matches the verified source fingerprint")
	}
	before, err := engine.observer.ObserveAbsent(ctx, request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	if before.State != ReplicaPartitionConverged {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: before}, errors.New("target partition is not proven empty and healthy on every expected replica")
	}
	if err := moveLedger(&entry, LedgerCommitPending, engine.now()); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry.Checkpoint, entry.LastError = "replace_pending", ""
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}

	query, err := BuildReplacePartitionSQL(request.Chunk.TargetDatabase, request.Chunk.TargetTable, request.Chunk.StagingTable, request.Chunk.Partition)
	if err != nil {
		return engine.markCommitUnknown(entry, ReplicaPartitionReport{}, err)
	}
	_, replaceErr := engine.client.Query(ctx, queryForEndpoint(request.Pair.Target, request.Chunk.TargetDatabase, query, FormatTSVRaw))
	// Once the statement has been issued, client cancellation cannot prove whether
	// the server accepted it. Reconciliation therefore observes state and never
	// blindly reissues the mutation.
	report, observeErr := engine.observer.ObserveSource(context.Background(), request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	if observeErr != nil {
		cause := fmt.Errorf("observe replicas after REPLACE PARTITION: %w", observeErr)
		if replaceErr != nil {
			cause = fmt.Errorf("REPLACE PARTITION error: %v; %w", replaceErr, cause)
		}
		return engine.markCommitUnknown(entry, report, cause)
	}
	return engine.classifyCommitObservation(entry, report, replaceErr)
}

func (engine *ReplicatedPartitionLabCommitEngine) ReconcileCommit(ctx context.Context, request ReplicatedPartitionLabCommitRequest) (ReplicatedPartitionLabCommitResult, error) {
	request, err := normalizeReplicatedPartitionLabRequest(request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	if entry.State == LedgerCommitted {
		return engine.reconcileCommitted(ctx, request, entry)
	}
	if entry.State != LedgerCommitPending && entry.State != LedgerCommitUnknown && entry.State != LedgerReplicasConverging {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("commit reconciliation requires commit_pending, commit_unknown or replicas_converging, got %s", entry.State)
	}
	report, err := engine.observer.ObserveSource(ctx, request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	result, reconcileErr := engine.classifyCommitObservation(entry, report, nil)
	if result.Entry.State == LedgerCommitted {
		result.RecoveredAmbiguous = true
	}
	return result, reconcileErr
}

func (engine *ReplicatedPartitionLabCommitEngine) classifyCommitObservation(entry LedgerEntry, report ReplicaPartitionReport, statementErr error) (ReplicatedPartitionLabCommitResult, error) {
	if report.State == ReplicaPartitionConverged {
		return engine.finishCommitted(entry, report, statementErr != nil || entry.State != LedgerCommitPending)
	}
	if report.State == ReplicaPartitionConflict || (report.Matched == 0 && report.Absent == report.Expected) {
		cause := errors.New("replica observations do not prove a committed or safely retryable REPLACE PARTITION")
		if statementErr != nil {
			cause = fmt.Errorf("REPLACE PARTITION error: %v; %w", statementErr, cause)
		}
		return engine.markCommitUnknown(entry, report, cause)
	}
	if entry.State != LedgerReplicasConverging {
		if err := moveLedger(&entry, LedgerReplicasConverging, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
	}
	entry.Checkpoint = "replicas_converging"
	if statementErr != nil {
		entry.LastError = statementErr.Error()
	} else {
		entry.LastError = ""
	}
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.New("replica convergence is pending; REPLACE PARTITION was not reissued")
}

func (engine *ReplicatedPartitionLabCommitEngine) Rollback(ctx context.Context, request ReplicatedPartitionLabCommitRequest) (ReplicatedPartitionLabCommitResult, error) {
	request, err := normalizeReplicatedPartitionLabRequest(request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	if entry.State == LedgerRolledBack {
		return engine.reconcileRolledBack(ctx, request, entry)
	}
	if entry.State == LedgerRollbackPending {
		return engine.ReconcileRollback(ctx, request)
	}
	if entry.State == LedgerCommitPending || entry.State == LedgerCommitUnknown || entry.State == LedgerReplicasConverging {
		reconciled, reconcileErr := engine.ReconcileCommit(ctx, request)
		if reconcileErr != nil && reconciled.Entry.State != LedgerCommitted {
			return reconciled, reconcileErr
		}
		entry = reconciled.Entry
	}
	if entry.State == LedgerRollbackBlocked || entry.State == LedgerRollbackFailed {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("automatic rollback is unavailable in state %s", entry.State)
	}
	if entry.State != LedgerCommitted {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("replicated partition rollback requires committed state, got %s", entry.State)
	}
	if err := engine.verifyGuard(ctx, request); err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	before, err := engine.observer.ObserveSource(ctx, request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	if before.State == ReplicaPartitionConflict {
		return engine.markRollbackBlocked(entry, before, errors.New("automatic rollback blocked because a destination replica contains data not owned by this run"))
	}
	if before.State != ReplicaPartitionConverged {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: before}, errors.New("automatic rollback waits until every expected replica proves the run-owned committed fingerprint")
	}
	if err := moveLedger(&entry, LedgerRollbackPending, engine.now()); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry.Checkpoint, entry.LastError = "drop_partition_pending", ""
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	query := fmt.Sprintf("ALTER TABLE %s.%s DROP PARTITION %s", quoteIdentifier(request.Chunk.TargetDatabase), quoteIdentifier(request.Chunk.TargetTable), quoteLiteral(request.Chunk.Partition))
	_, dropErr := engine.client.Query(ctx, queryForEndpoint(request.Pair.Target, request.Chunk.TargetDatabase, query, FormatTSVRaw))
	report, observeErr := engine.observer.ObserveAbsent(context.Background(), request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	if observeErr != nil {
		cause := fmt.Errorf("observe replicas after DROP PARTITION: %w", observeErr)
		if dropErr != nil {
			cause = fmt.Errorf("DROP PARTITION error: %v; %w", dropErr, cause)
		}
		return engine.markRollbackPending(entry, report, cause)
	}
	if report.State == ReplicaPartitionConverged {
		if err := moveLedger(&entry, LedgerRolledBack, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
		entry.Checkpoint, entry.LastError = "drop_partition_verified", ""
		if err := engine.ledger.Put(context.Background(), entry); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report, RecoveredAmbiguous: dropErr != nil}, nil
	}
	if report.State == ReplicaPartitionConflict {
		return engine.markRollbackFailed(entry, report, errors.New("rollback verification found unexpected partition data on a destination replica"))
	}
	entry.Checkpoint = "rollback_replicas_converging"
	if dropErr != nil {
		entry.LastError = dropErr.Error()
	}
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.New("rollback convergence is pending; DROP PARTITION was not reissued")
}

func (engine *ReplicatedPartitionLabCommitEngine) ReconcileRollback(ctx context.Context, request ReplicatedPartitionLabCommitRequest) (ReplicatedPartitionLabCommitResult, error) {
	request, err := normalizeReplicatedPartitionLabRequest(request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	if entry.State == LedgerRolledBack {
		return engine.reconcileRolledBack(ctx, request, entry)
	}
	if entry.State != LedgerRollbackPending {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, fmt.Errorf("rollback reconciliation requires rollback_pending state, got %s", entry.State)
	}
	report, err := engine.observer.ObserveAbsent(ctx, request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	if err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry}, err
	}
	if report.State == ReplicaPartitionConverged {
		if err := moveLedger(&entry, LedgerRolledBack, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
		entry.Checkpoint, entry.LastError = "rollback_reconciled_absent", ""
		if err := engine.ledger.Put(ctx, entry); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report, RecoveredAmbiguous: true}, nil
	}
	if report.State == ReplicaPartitionConflict {
		return engine.markRollbackFailed(entry, report, errors.New("rollback reconciliation found unexpected partition data"))
	}
	entry.Checkpoint, entry.LastError = "rollback_replicas_converging", ""
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.New("rollback convergence is still pending; DROP PARTITION was not reissued")
}

func normalizeReplicatedPartitionLabRequest(request ReplicatedPartitionLabCommitRequest) (ReplicatedPartitionLabCommitRequest, error) {
	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return request, err
	}
	request.Pair = pair
	if err := ValidateTransferChunk(request.Chunk); err != nil {
		return request, err
	}
	if request.Chunk.Strategy != StrategyNativeStream {
		return request, errors.New("replicated partition lab commit requires native_stream staging")
	}
	if strings.TrimSpace(request.Chunk.Partition) == "" || request.Chunk.Filter != nil {
		return request, errors.New("replicated partition lab commit requires exactly one partition and no time filter")
	}
	if request.Chunk.TargetDatabase != pair.Database || request.TargetTable.Name != request.Chunk.TargetTable {
		return request, errors.New("replicated partition lab request target does not match the frozen pair plan")
	}
	if !request.TargetTable.IsReplicated {
		return request, errors.New("replicated partition lab request requires a replicated target table")
	}
	return request, nil
}

func (engine *ReplicatedPartitionLabCommitEngine) loadEntry(ctx context.Context, request ReplicatedPartitionLabCommitRequest) (LedgerEntry, error) {
	key := LedgerKey{RunID: request.Chunk.RunID, Database: request.Chunk.TargetDatabase, Table: request.Chunk.TargetTable, Partition: request.Chunk.Partition, Chunk: request.Chunk.Sequence}
	entry, ok, err := engine.ledger.Get(ctx, key)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("read replicated partition migration ledger: %w", err)
	}
	if !ok {
		return LedgerEntry{}, errors.New("replicated partition migration ledger entry does not exist")
	}
	if err := validateLedgerOwnership(entry, request.Chunk); err != nil {
		return entry, fmt.Errorf("validate replicated partition ledger ownership: %w", err)
	}
	return entry, nil
}

func (engine *ReplicatedPartitionLabCommitEngine) verifyGuard(ctx context.Context, request ReplicatedPartitionLabCommitRequest) error {
	return engine.guard.Verify(ctx, CommitGuardRequest{RunID: request.Chunk.RunID, Target: request.Pair.Target, Database: request.Chunk.TargetDatabase, TargetTable: request.Chunk.TargetTable, StagingTable: request.Chunk.StagingTable})
}

func (engine *ReplicatedPartitionLabCommitEngine) reconcileCommitted(ctx context.Context, request ReplicatedPartitionLabCommitRequest, entry LedgerEntry) (ReplicatedPartitionLabCommitResult, error) {
	report, err := engine.observer.ObserveSource(ctx, request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	result := ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report, RollbackAvailable: err == nil && report.State == ReplicaPartitionConverged}
	if err != nil {
		return result, err
	}
	if report.State != ReplicaPartitionConverged {
		return result, errors.New("ledger is committed but destination replicas no longer prove convergence")
	}
	return result, nil
}

func (engine *ReplicatedPartitionLabCommitEngine) reconcileRolledBack(ctx context.Context, request ReplicatedPartitionLabCommitRequest, entry LedgerEntry) (ReplicatedPartitionLabCommitResult, error) {
	report, err := engine.observer.ObserveAbsent(ctx, request.TargetSnapshot, request.Pair.Target, request.Chunk.TargetDatabase, request.TargetTable, request.Chunk.Partition, entry.Source)
	result := ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}
	if err != nil {
		return result, err
	}
	if report.State != ReplicaPartitionConverged {
		return result, errors.New("ledger is rolled_back but destination replicas do not prove the partition is absent")
	}
	return result, nil
}

func (engine *ReplicatedPartitionLabCommitEngine) finishCommitted(entry LedgerEntry, report ReplicaPartitionReport, recovered bool) (ReplicatedPartitionLabCommitResult, error) {
	if entry.State == LedgerCommitPending {
		if err := moveLedger(&entry, LedgerReplicasConverging, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
	}
	if entry.State != LedgerCommitted {
		if err := moveLedger(&entry, LedgerCommitted, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
	}
	entry.Target = entry.Source
	entry.Checkpoint, entry.LastError = "replicas_verified_committed", ""
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report, RollbackAvailable: true, RecoveredAmbiguous: recovered}, nil
}

func (engine *ReplicatedPartitionLabCommitEngine) markCommitUnknown(entry LedgerEntry, report ReplicaPartitionReport, cause error) (ReplicatedPartitionLabCommitResult, error) {
	if entry.State != LedgerCommitUnknown {
		if err := moveLedger(&entry, LedgerCommitUnknown, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
	}
	entry.Checkpoint, entry.LastError = "commit_unknown", cause.Error()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.Join(cause, fmt.Errorf("persist replicated commit_unknown state: %w", err))
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, cause
}

func (engine *ReplicatedPartitionLabCommitEngine) markRollbackBlocked(entry LedgerEntry, report ReplicaPartitionReport, cause error) (ReplicatedPartitionLabCommitResult, error) {
	if entry.State != LedgerRollbackBlocked {
		if err := moveLedger(&entry, LedgerRollbackBlocked, engine.now()); err != nil {
			return ReplicatedPartitionLabCommitResult{}, err
		}
	}
	entry.Checkpoint, entry.LastError = "rollback_blocked", cause.Error()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.Join(cause, fmt.Errorf("persist replicated rollback_blocked state: %w", err))
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, cause
}

func (engine *ReplicatedPartitionLabCommitEngine) markRollbackFailed(entry LedgerEntry, report ReplicaPartitionReport, cause error) (ReplicatedPartitionLabCommitResult, error) {
	if entry.State != LedgerRollbackPending {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, fmt.Errorf("cannot mark rollback failed from %s: %w", entry.State, cause)
	}
	if err := moveLedger(&entry, LedgerRollbackFailed, engine.now()); err != nil {
		return ReplicatedPartitionLabCommitResult{}, err
	}
	entry.Checkpoint, entry.LastError = "rollback_failed", cause.Error()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.Join(cause, fmt.Errorf("persist replicated rollback_failed state: %w", err))
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, cause
}

func (engine *ReplicatedPartitionLabCommitEngine) markRollbackPending(entry LedgerEntry, report ReplicaPartitionReport, cause error) (ReplicatedPartitionLabCommitResult, error) {
	if entry.State != LedgerRollbackPending {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, fmt.Errorf("cannot preserve ambiguous replicated rollback from %s: %w", entry.State, cause)
	}
	entry.Checkpoint, entry.LastError, entry.UpdatedAt = "rollback_observation_pending", cause.Error(), engine.now()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, errors.Join(cause, fmt.Errorf("persist ambiguous replicated rollback state: %w", err))
	}
	return ReplicatedPartitionLabCommitResult{Entry: entry, Replicas: report}, cause
}
