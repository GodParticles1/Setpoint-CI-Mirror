package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type NativeChunkExecution struct {
	Pair        PairParameters
	Chunk       TransferChunk
	SourceTable Table
	TargetTable Table
}

type ChunkExecutionResult struct {
	Entry        LedgerEntry        `json:"entry"`
	Verification VerificationResult `json:"verification"`
	Transferred  bool               `json:"transferred"`
	Idempotent   bool               `json:"idempotent"`
	Reconciled   bool               `json:"reconciled"`
}

type NativeExecutionEngine struct {
	ledger    LedgerStore
	staging   StagingController
	transport NativeTransport
	verifier  FingerprintVerifier
	now       func() time.Time
}

func NewNativeExecutionEngine(ledger LedgerStore, staging StagingController, transport NativeTransport, verifier FingerprintVerifier) (*NativeExecutionEngine, error) {
	if ledger == nil || staging == nil || transport == nil || verifier == nil {
		return nil, errors.New("ledger, staging, transport and verifier are required")
	}
	return &NativeExecutionEngine{ledger: ledger, staging: staging, transport: transport, verifier: verifier, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (engine *NativeExecutionEngine) Execute(ctx context.Context, request NativeChunkExecution) (ChunkExecutionResult, error) {
	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return ChunkExecutionResult{}, err
	}
	request.Pair = pair
	if err := ValidateTransferChunk(request.Chunk); err != nil {
		return ChunkExecutionResult{}, err
	}
	if request.Chunk.Strategy != StrategyNativeStream {
		return ChunkExecutionResult{}, errors.New("native execution engine only accepts native_stream chunks")
	}

	key := LedgerKey{RunID: request.Chunk.RunID, Database: request.Chunk.TargetDatabase, Table: request.Chunk.TargetTable, Partition: request.Chunk.Partition, Chunk: request.Chunk.Sequence}
	entry, exists, err := engine.ledger.Get(ctx, key)
	if err != nil {
		return ChunkExecutionResult{}, fmt.Errorf("read migration ledger: %w", err)
	}
	if exists {
		if err := validateLedgerOwnership(entry, request.Chunk); err != nil {
			return ChunkExecutionResult{Entry: entry}, err
		}
		switch entry.State {
		case LedgerVerified:
			reconciled, reconcileErr := engine.Reconcile(ctx, request, entry)
			if reconcileErr != nil {
				return ChunkExecutionResult{Entry: reconciled.Entry}, fmt.Errorf("reconcile verified Native staging: %w", reconcileErr)
			}
			if reconciled.Action != NativeReconcileReuseVerifiedStage {
				return ChunkExecutionResult{Entry: reconciled.Entry}, errors.New("verified Native staging could not be proven from runtime state")
			}
			return ChunkExecutionResult{Entry: reconciled.Entry, Verification: reconciled.Verification, Idempotent: true, Reconciled: true}, nil
		case LedgerCommitted:
			return ChunkExecutionResult{Entry: entry}, errors.New("migration chunk is committed; transfer execution is blocked and commit/rollback reconciliation is required")
		case LedgerRolledBack, LedgerRollbackFailed:
			return ChunkExecutionResult{Entry: entry}, fmt.Errorf("migration chunk is terminal in state %s; use a new run ID for another attempt", entry.State)
		case LedgerFailed:
			return engine.reconcileStagingCleanup(ctx, entry, pair.Target, request.Chunk, ledgerFailure(entry))
		case LedgerRollbackPending:
			if entry.Checkpoint != "staging_drop_pending" {
				return ChunkExecutionResult{Entry: entry}, fmt.Errorf("migration chunk requires rollback reconciliation from checkpoint %q; staging cleanup is blocked", entry.Checkpoint)
			}
			return engine.reconcileStagingCleanup(ctx, entry, pair.Target, request.Chunk, ledgerFailure(entry))
		case LedgerCommitPending, LedgerReplicasConverging, LedgerCommitUnknown, LedgerRollbackBlocked:
			return ChunkExecutionResult{Entry: entry}, fmt.Errorf("migration chunk requires commit/rollback reconciliation from state %s; transfer retry is blocked", entry.State)
		case LedgerPlanned, LedgerStaging, LedgerTransferred:
			reconciled, reconcileErr := engine.Reconcile(ctx, request, entry)
			if reconcileErr != nil {
				return ChunkExecutionResult{Entry: reconciled.Entry}, fmt.Errorf("reconcile interrupted Native transfer: %w", reconcileErr)
			}
			switch reconciled.Action {
			case NativeReconcileReuseVerifiedStage:
				return ChunkExecutionResult{Entry: reconciled.Entry, Verification: reconciled.Verification, Idempotent: true, Reconciled: true}, nil
			case NativeReconcileRestartCleanStaging:
				entry = reconciled.Entry
			case NativeReconcileBlocked:
				return ChunkExecutionResult{Entry: reconciled.Entry}, errors.New(reconciled.Reason)
			}
		}
	} else {
		entry = LedgerEntry{Key: key, Strategy: StrategyNativeStream, State: LedgerPlanned, Attempt: 1, StagingTable: request.Chunk.StagingTable, UpdatedAt: engine.now()}
		if err := validateLedgerOwnership(entry, request.Chunk); err != nil {
			return ChunkExecutionResult{}, err
		}
	}

	if exists {
		entry.Attempt++
	}
	sourceFingerprint, err := fingerprintChunk(ctx, engine.verifier, pair.Source, request.Chunk.SourceDatabase, request.SourceTable, request.Chunk)
	if err != nil {
		return ChunkExecutionResult{Entry: entry}, fmt.Errorf("fingerprint source before staging mutation: %w", err)
	}
	if ledgerSourceFingerprintRecorded(entry) && !CompareFingerprints(entry.Source, sourceFingerprint).Passed {
		return ChunkExecutionResult{Entry: entry}, errors.New("source fingerprint changed before staging mutation; a new run is required")
	}
	entry.Source = sourceFingerprint
	entry.Checkpoint, entry.LastError, entry.UpdatedAt = "staging_recreate_intent", "", engine.now()
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ChunkExecutionResult{Entry: entry}, fmt.Errorf("persist staging recreation intent: %w", err)
	}
	if err := engine.staging.Recreate(ctx, pair.Target, request.Chunk.TargetDatabase, request.Chunk.StagingTable, request.Chunk.TargetTable); err != nil {
		return engine.failAndRollback(ctx, entry, pair.Target, request.Chunk, fmt.Errorf("recreate staging: %w", err))
	}
	if entry.State == LedgerPlanned {
		if err := moveLedger(&entry, LedgerStaging, engine.now()); err != nil {
			return ChunkExecutionResult{}, err
		}
	} else {
		entry.State, entry.UpdatedAt, entry.LastError = LedgerStaging, engine.now(), ""
	}
	entry.Checkpoint = "staging_ready"
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ChunkExecutionResult{Entry: entry}, fmt.Errorf("persist ready staging state: %w", err)
	}

	entry.Checkpoint = "native_transfer_intent"
	entry.UpdatedAt = engine.now()
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ChunkExecutionResult{Entry: entry}, fmt.Errorf("persist Native transfer intent: %w", err)
	}

	transferResult, transferErr := engine.transport.Transfer(ctx, NativeTransferRequest{Source: pair.Source, Target: pair.Target, Chunk: request.Chunk, SourceTable: request.SourceTable})
	if transferErr != nil {
		entry.LastError, entry.UpdatedAt = transferErr.Error(), engine.now()
		if ctx.Err() != nil {
			if err := engine.ledger.Put(context.Background(), entry); err != nil {
				return ChunkExecutionResult{Entry: entry}, errors.Join(transferErr, fmt.Errorf("persist interrupted Native transfer state: %w", err))
			}
			return ChunkExecutionResult{Entry: entry}, transferErr
		}
		return engine.failAndRollback(ctx, entry, pair.Target, request.Chunk, transferErr)
	}
	entry.Checkpoint = fmt.Sprintf("native_bytes=%d", transferResult.BytesTransferred)
	if err := moveLedger(&entry, LedgerTransferred, engine.now()); err != nil {
		return ChunkExecutionResult{}, err
	}
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ChunkExecutionResult{}, err
	}

	stagingTable := request.TargetTable
	stagingTable.Name = request.Chunk.StagingTable
	targetFingerprint, err := fingerprintChunk(ctx, engine.verifier, pair.Target, request.Chunk.TargetDatabase, stagingTable, request.Chunk)
	if err != nil {
		return engine.failAndRollback(ctx, entry, pair.Target, request.Chunk, fmt.Errorf("fingerprint staging: %w", err))
	}
	entry.Target = targetFingerprint
	verification := CompareFingerprints(entry.Source, entry.Target)
	if !verification.Passed {
		return engine.failAndRollback(ctx, entry, pair.Target, request.Chunk, errors.New(verification.Reason))
	}
	if err := moveLedger(&entry, LedgerVerified, engine.now()); err != nil {
		return ChunkExecutionResult{}, err
	}
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ChunkExecutionResult{}, err
	}
	return ChunkExecutionResult{Entry: entry, Verification: verification, Transferred: true}, nil
}

func fingerprintChunk(ctx context.Context, verifier FingerprintVerifier, endpoint Endpoint, database string, table Table, chunk TransferChunk) (DataFingerprint, error) {
	if chunk.Partition == "" {
		return verifier.Fingerprint(ctx, endpoint, database, table, chunk.Filter)
	}
	partitionVerifier, ok := verifier.(PartitionFingerprintVerifier)
	if !ok {
		return DataFingerprint{}, errors.New("partition-scoped migration requires a partition-aware fingerprint verifier")
	}
	return partitionVerifier.FingerprintPartition(ctx, endpoint, database, table, chunk.Partition)
}

func (engine *NativeExecutionEngine) failAndRollback(ctx context.Context, entry LedgerEntry, endpoint Endpoint, chunk TransferChunk, cause error) (ChunkExecutionResult, error) {
	entry.LastError = cause.Error()
	if entry.State != LedgerFailed {
		if err := moveLedger(&entry, LedgerFailed, engine.now()); err != nil {
			return ChunkExecutionResult{Entry: entry}, errors.Join(cause, err)
		}
	}
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist failed Native staging state: %w", err))
	}
	return engine.reconcileStagingCleanup(ctx, entry, endpoint, chunk, cause)
}

func (engine *NativeExecutionEngine) reconcileStagingCleanup(ctx context.Context, entry LedgerEntry, endpoint Endpoint, chunk TransferChunk, cause error) (ChunkExecutionResult, error) {
	return reconcileNativeStagingCleanup(ctx, engine.ledger, engine.staging, engine.now, entry, endpoint, chunk, cause)
}

func reconcileNativeStagingCleanup(ctx context.Context, ledger LedgerStore, staging StagingController, now func() time.Time, entry LedgerEntry, endpoint Endpoint, chunk TransferChunk, cause error) (ChunkExecutionResult, error) {
	if err := validateLedgerOwnership(entry, chunk); err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("Native staging cleanup ownership: %w", err))
	}
	switch entry.State {
	case LedgerPlanned, LedgerFailed, LedgerStaging, LedgerTransferred, LedgerVerified, LedgerRollbackPending:
	default:
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("staging cleanup is unavailable from ledger state %s", entry.State))
	}
	if entry.State == LedgerRollbackPending && entry.Checkpoint != "staging_drop_pending" {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("staging cleanup intent does not match rollback checkpoint %q", entry.Checkpoint))
	}
	inspector, ok := staging.(StagingInspector)
	if !ok {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, errors.New("staging implementation cannot verify cleanup; rollback remains pending"))
	}
	if entry.State != LedgerRollbackPending {
		if entry.State == LedgerPlanned {
			if err := moveLedger(&entry, LedgerFailed, now()); err != nil {
				return ChunkExecutionResult{Entry: entry}, errors.Join(cause, err)
			}
		}
		if err := moveLedger(&entry, LedgerRollbackPending, now()); err != nil {
			return ChunkExecutionResult{Entry: entry}, errors.Join(cause, err)
		}
		entry.Checkpoint = "staging_drop_pending"
		if err := ledger.Put(context.Background(), entry); err != nil {
			return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist Native staging rollback intent: %w", err))
		}
	}

	exists, err := inspector.Exists(ctx, endpoint, chunk.TargetDatabase, chunk.StagingTable)
	if err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("inspect run-owned staging before cleanup: %w", err))
	}
	if exists {
		dropErr := staging.Drop(ctx, endpoint, chunk.TargetDatabase, chunk.StagingTable)
		exists, err = inspector.Exists(context.Background(), endpoint, chunk.TargetDatabase, chunk.StagingTable)
		if err != nil {
			return ChunkExecutionResult{Entry: entry}, errors.Join(cause, dropErr, fmt.Errorf("verify run-owned staging cleanup: %w", err))
		}
		if exists {
			if dropErr != nil {
				return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("drop run-owned staging: %w", dropErr))
			}
			return markStagingRollbackFailed(ledger, now, entry, cause, errors.New("run-owned staging still exists after DROP returned success"))
		}
		if dropErr != nil {
			entry.Checkpoint = "staging_drop_reconciled_absent"
		}
	}
	if err := moveLedger(&entry, LedgerRolledBack, now()); err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, err)
	}
	if entry.Checkpoint != "staging_drop_reconciled_absent" {
		entry.Checkpoint = "staging_cleanup_verified"
	}
	if err := ledger.Put(context.Background(), entry); err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist verified Native staging cleanup: %w", err))
	}
	return ChunkExecutionResult{Entry: entry}, cause
}

func markStagingRollbackFailed(ledger LedgerStore, now func() time.Time, entry LedgerEntry, cause, rollbackErr error) (ChunkExecutionResult, error) {
	if err := moveLedger(&entry, LedgerRollbackFailed, now()); err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, rollbackErr, err)
	}
	entry.LastError = errors.Join(cause, rollbackErr).Error()
	entry.Checkpoint = "staging_cleanup_failed"
	if err := ledger.Put(context.Background(), entry); err != nil {
		return ChunkExecutionResult{Entry: entry}, errors.Join(cause, rollbackErr, fmt.Errorf("persist Native staging rollback failure: %w", err))
	}
	return ChunkExecutionResult{Entry: entry}, errors.Join(cause, rollbackErr)
}

func ledgerFailure(entry LedgerEntry) error {
	if entry.LastError != "" {
		return errors.New(entry.LastError)
	}
	return fmt.Errorf("resume Native staging cleanup from ledger state %s", entry.State)
}

func moveLedger(entry *LedgerEntry, to LedgerState, now time.Time) error {
	if err := ValidateLedgerTransition(entry.State, to); err != nil {
		return err
	}
	entry.State, entry.UpdatedAt = to, now
	return nil
}
