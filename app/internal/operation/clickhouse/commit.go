package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type CommitGuardRequest struct {
	RunID        string   `json:"run_id"`
	Target       Endpoint `json:"target"`
	Database     string   `json:"database"`
	TargetTable  string   `json:"target_table"`
	StagingTable string   `json:"staging_table"`
}

type CommitGuard interface {
	Verify(context.Context, CommitGuardRequest) error
}

type ExchangeCommitRequest struct {
	Pair           PairParameters
	Chunk          TransferChunk
	TargetTable    Table
	TargetBaseline *DataFingerprint
}

type ExchangeCommitResult struct {
	Entry              LedgerEntry        `json:"entry"`
	Verification       VerificationResult `json:"verification"`
	RollbackAvailable  bool               `json:"rollback_available"`
	RecoveredAmbiguous bool               `json:"recovered_ambiguous"`
}

type AtomicExchangeCommitEngine struct {
	ledger   LedgerStore
	client   QueryClient
	verifier FingerprintVerifier
	guard    CommitGuard
	now      func() time.Time
}

func NewAtomicExchangeCommitEngine(ledger LedgerStore, client QueryClient, verifier FingerprintVerifier, guard CommitGuard) (*AtomicExchangeCommitEngine, error) {
	if ledger == nil || client == nil || verifier == nil || guard == nil {
		return nil, errors.New("ledger, query client, verifier and commit guard are required")
	}
	return &AtomicExchangeCommitEngine{ledger: ledger, client: client, verifier: verifier, guard: guard, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (engine *AtomicExchangeCommitEngine) Commit(ctx context.Context, request ExchangeCommitRequest) (ExchangeCommitResult, error) {
	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return ExchangeCommitResult{}, err
	}
	request.Pair = pair
	if err := validateExchangeCommitRequest(request); err != nil {
		return ExchangeCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	if entry.State == LedgerCommitted {
		return engine.observeCommitted(ctx, pair.Target, request, entry)
	}
	if entry.State == LedgerCommitUnknown {
		reconciled, reconcileErr := engine.Reconcile(ctx, request)
		if reconcileErr != nil {
			return reconciled, reconcileErr
		}
		if reconciled.Entry.State != LedgerCommitted {
			return reconciled, errors.New("EXCHANGE intent was reconciled as uncommitted; a new explicit execution attempt is required")
		}
		return reconciled, nil
	}
	if entry.State != LedgerVerified {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("commit requires ledger state verified, got %s", entry.State)
	}
	currentSource, err := engine.sourceFingerprint(ctx, pair.Source, request)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("fingerprint source before commit: %w", err)
	}
	if !CompareFingerprints(entry.Source, currentSource).Passed {
		return ExchangeCommitResult{Entry: entry}, errors.New("source fingerprint changed after staging verification; commit is blocked")
	}
	if entry.Source.Rows == 0 {
		if baseline := targetBaseline(request); baseline.Rows != 0 {
			return ExchangeCommitResult{Entry: entry}, errors.New("empty-source replacement of a non-empty target is outside the bounded Apply path")
		}
		observed, err := engine.observe(ctx, pair.Target, request, entry.Source)
		if err != nil {
			return ExchangeCommitResult{Entry: entry}, fmt.Errorf("observe empty-source no-op state: %w", err)
		}
		if observed.state != exchangeObservedPrepared || observed.target.Rows != 0 || observed.staging.Rows != 0 {
			return ExchangeCommitResult{Entry: entry}, errors.New("empty-source no-op is blocked because target or staging runtime state changed")
		}
		if err := moveLedger(&entry, LedgerCommitted, engine.now()); err != nil {
			return ExchangeCommitResult{}, err
		}
		entry.Checkpoint = "empty_source_noop"
		if err := engine.ledger.Put(ctx, entry); err != nil {
			return ExchangeCommitResult{}, err
		}
		return ExchangeCommitResult{Entry: entry, Verification: CompareFingerprints(entry.Source, entry.Target)}, nil
	}
	if err := engine.verifyGuard(ctx, request); err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	capability, err := InspectCommitCapability(ctx, engine.client, pair.Target, request.Chunk.TargetDatabase, request.Chunk.TargetTable)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	if !capability.ExchangeTables {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("ClickHouse atomic exchange commit is unavailable: %s", capability.Reason)
	}
	before, err := engine.observe(ctx, pair.Target, request, entry.Source)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("observe target and staging before commit: %w", err)
	}
	if before.state != exchangeObservedPrepared {
		return ExchangeCommitResult{Entry: entry}, errors.New("target or staging fingerprint changed after verification; commit is blocked")
	}
	if err := moveLedger(&entry, LedgerCommitUnknown, engine.now()); err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	entry.Checkpoint, entry.LastError = "exchange_intent", ""
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("persist EXCHANGE intent before commit: %w", err)
	}

	exchangeErr := engine.exchange(ctx, pair.Target, request.Chunk.TargetDatabase, request.Chunk.TargetTable, request.Chunk.StagingTable)
	observed, observeErr := engine.observe(ctx, pair.Target, request, entry.Source)
	if observeErr != nil {
		if exchangeErr != nil {
			return engine.markCommitUnknown(entry, fmt.Errorf("exchange error: %v; observation error: %w", exchangeErr, observeErr))
		}
		return engine.markCommitUnknown(entry, fmt.Errorf("exchange returned success but commit observation failed: %w", observeErr))
	}

	switch observed.state {
	case exchangeObservedCommitted:
		if err := moveLedger(&entry, LedgerCommitted, engine.now()); err != nil {
			return ExchangeCommitResult{}, err
		}
		entry.Target = observed.target
		if exchangeErr != nil {
			entry.Checkpoint = "exchange_observed_committed_after_error"
		} else {
			entry.Checkpoint = "exchange_committed"
		}
		entry.LastError = ""
		if err := engine.ledger.Put(ctx, entry); err != nil {
			return ExchangeCommitResult{}, err
		}
		return ExchangeCommitResult{Entry: entry, Verification: CompareFingerprints(entry.Source, observed.target), RollbackAvailable: true, RecoveredAmbiguous: exchangeErr != nil}, nil
	case exchangeObservedPrepared:
		if exchangeErr == nil {
			return engine.markCommitUnknown(entry, errors.New("EXCHANGE returned success but tables still look uncommitted"))
		}
		if err := moveLedger(&entry, LedgerVerified, engine.now()); err != nil {
			return ExchangeCommitResult{Entry: entry}, errors.Join(exchangeErr, err)
		}
		entry.Checkpoint, entry.LastError = "exchange_observed_uncommitted_after_error", exchangeErr.Error()
		if err := engine.ledger.Put(context.Background(), entry); err != nil {
			return ExchangeCommitResult{Entry: entry}, errors.Join(exchangeErr, fmt.Errorf("persist observed uncommitted EXCHANGE state: %w", err))
		}
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("EXCHANGE is proven unapplied; a new explicit execution attempt is required: %w", exchangeErr)
	default:
		cause := errors.New("post-EXCHANGE fingerprints do not prove committed or uncommitted state")
		if exchangeErr != nil {
			cause = fmt.Errorf("%v: %w", exchangeErr, cause)
		}
		return engine.markCommitUnknown(entry, cause)
	}
}

func (engine *AtomicExchangeCommitEngine) Reconcile(ctx context.Context, request ExchangeCommitRequest) (ExchangeCommitResult, error) {
	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return ExchangeCommitResult{}, err
	}
	request.Pair = pair
	if err := validateExchangeCommitRequest(request); err != nil {
		return ExchangeCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	if entry.State != LedgerCommitUnknown {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("reconcile requires commit_unknown state, got %s", entry.State)
	}
	observed, err := engine.observe(ctx, pair.Target, request, entry.Source)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	switch observed.state {
	case exchangeObservedCommitted:
		if err := moveLedger(&entry, LedgerCommitted, engine.now()); err != nil {
			return ExchangeCommitResult{}, err
		}
		entry.Target, entry.Checkpoint, entry.LastError = observed.target, "commit_reconciled_committed", ""
	case exchangeObservedPrepared:
		if err := moveLedger(&entry, LedgerVerified, engine.now()); err != nil {
			return ExchangeCommitResult{}, err
		}
		entry.Checkpoint, entry.LastError = "commit_reconciled_uncommitted", ""
	default:
		return ExchangeCommitResult{Entry: entry}, errors.New("commit remains ambiguous after reconciliation")
	}
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ExchangeCommitResult{}, err
	}
	return ExchangeCommitResult{Entry: entry, Verification: CompareFingerprints(entry.Source, observed.target), RollbackAvailable: entry.State == LedgerCommitted, RecoveredAmbiguous: true}, nil
}

func (engine *AtomicExchangeCommitEngine) Rollback(ctx context.Context, request ExchangeCommitRequest) (ExchangeCommitResult, error) {
	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return ExchangeCommitResult{}, err
	}
	request.Pair = pair
	if err := validateExchangeCommitRequest(request); err != nil {
		return ExchangeCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	if entry.State == LedgerRolledBack {
		return engine.observeRolledBack(ctx, pair.Target, request, entry)
	}
	if entry.State == LedgerRollbackPending {
		return engine.ReconcileRollback(ctx, request)
	}
	if entry.State == LedgerRollbackBlocked || entry.State == LedgerRollbackFailed {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("automatic rollback is unavailable in state %s", entry.State)
	}
	if entry.State == LedgerCommitUnknown {
		reconciled, reconcileErr := engine.Reconcile(ctx, request)
		if reconcileErr != nil {
			return reconciled, reconcileErr
		}
		entry = reconciled.Entry
	}
	if entry.State != LedgerCommitted {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("post-commit rollback requires committed state, got %s", entry.State)
	}
	if err := engine.verifyGuard(ctx, request); err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	observed, err := engine.observe(ctx, pair.Target, request, entry.Source)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("observe committed state before rollback: %w", err)
	}
	if observed.state != exchangeObservedCommitted {
		return engine.markRollbackBlocked(entry, errors.New("automatic rollback blocked because the committed target changed after cutover"))
	}
	if err := moveLedger(&entry, LedgerRollbackPending, engine.now()); err != nil {
		return ExchangeCommitResult{}, err
	}
	entry.Checkpoint, entry.LastError = "rollback_exchange_intent", ""
	if err := engine.ledger.Put(ctx, entry); err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("persist rollback EXCHANGE intent: %w", err)
	}
	exchangeErr := engine.exchange(ctx, pair.Target, request.Chunk.TargetDatabase, request.Chunk.TargetTable, request.Chunk.StagingTable)
	observed, observeErr := engine.observe(context.Background(), pair.Target, request, entry.Source)
	if observeErr != nil {
		cause := fmt.Errorf("observe state after rollback EXCHANGE: %w", observeErr)
		if exchangeErr != nil {
			cause = fmt.Errorf("rollback EXCHANGE error: %v; %w", exchangeErr, cause)
		}
		return engine.markRollbackPending(entry, cause)
	}
	return engine.classifyRollbackObservation(entry, observed, exchangeErr, false)
}

func (engine *AtomicExchangeCommitEngine) ReconcileRollback(ctx context.Context, request ExchangeCommitRequest) (ExchangeCommitResult, error) {
	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return ExchangeCommitResult{}, err
	}
	request.Pair = pair
	if err := validateExchangeCommitRequest(request); err != nil {
		return ExchangeCommitResult{}, err
	}
	entry, err := engine.loadEntry(ctx, request)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, err
	}
	if entry.State == LedgerRolledBack {
		return engine.observeRolledBack(ctx, pair.Target, request, entry)
	}
	if entry.State != LedgerRollbackPending {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("rollback reconciliation requires rollback_pending state, got %s", entry.State)
	}
	if entry.Checkpoint != "rollback_exchange_intent" && entry.Checkpoint != "rollback_observation_pending" {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("rollback reconciliation does not own checkpoint %q", entry.Checkpoint)
	}
	observed, err := engine.observe(ctx, pair.Target, request, entry.Source)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("observe rollback reconciliation state: %w", err)
	}
	return engine.classifyRollbackObservation(entry, observed, nil, true)
}

func (engine *AtomicExchangeCommitEngine) loadEntry(ctx context.Context, request ExchangeCommitRequest) (LedgerEntry, error) {
	key := LedgerKey{RunID: request.Chunk.RunID, Database: request.Chunk.TargetDatabase, Table: request.Chunk.TargetTable, Partition: request.Chunk.Partition, Chunk: request.Chunk.Sequence}
	entry, ok, err := engine.ledger.Get(ctx, key)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("read migration ledger: %w", err)
	}
	if !ok {
		return LedgerEntry{}, errors.New("migration ledger entry does not exist")
	}
	if err := validateLedgerOwnership(entry, request.Chunk); err != nil {
		return entry, fmt.Errorf("validate migration ledger ownership: %w", err)
	}
	return entry, nil
}

func validateExchangeCommitRequest(request ExchangeCommitRequest) error {
	if err := ValidateTransferChunk(request.Chunk); err != nil {
		return err
	}
	if request.Chunk.Partition != "" || request.Chunk.Filter != nil {
		return errors.New("Atomic EXCHANGE commit requires whole-table scope without partition or time filter")
	}
	if request.TargetTable.Database != request.Chunk.TargetDatabase || request.TargetTable.Name != request.Chunk.TargetTable {
		return errors.New("commit target table identity does not match the frozen transfer chunk")
	}
	if request.TargetBaseline != nil && request.TargetBaseline.Rows > 0 && (request.TargetBaseline.HashSum64 == "" || request.TargetBaseline.HashXor64 == "") {
		return errors.New("non-empty commit target baseline requires dual hashes")
	}
	return nil
}

func (engine *AtomicExchangeCommitEngine) verifyGuard(ctx context.Context, request ExchangeCommitRequest) error {
	return engine.guard.Verify(ctx, CommitGuardRequest{RunID: request.Chunk.RunID, Target: request.Pair.Target, Database: request.Chunk.TargetDatabase, TargetTable: request.Chunk.TargetTable, StagingTable: request.Chunk.StagingTable})
}

func (engine *AtomicExchangeCommitEngine) sourceFingerprint(ctx context.Context, endpoint Endpoint, request ExchangeCommitRequest) (DataFingerprint, error) {
	sourceTable := request.TargetTable
	sourceTable.Database = request.Chunk.SourceDatabase
	sourceTable.Name = request.Chunk.SourceTable
	return fingerprintChunk(ctx, engine.verifier, endpoint, request.Chunk.SourceDatabase, sourceTable, request.Chunk)
}

func (engine *AtomicExchangeCommitEngine) observeCommitted(ctx context.Context, endpoint Endpoint, request ExchangeCommitRequest, entry LedgerEntry) (ExchangeCommitResult, error) {
	observed, err := engine.observe(ctx, endpoint, request, entry.Source)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("observe committed runtime state: %w", err)
	}
	if observed.state != exchangeObservedCommitted {
		return ExchangeCommitResult{Entry: entry}, errors.New("ledger is committed but runtime fingerprints do not prove the committed state")
	}
	return ExchangeCommitResult{Entry: entry, Verification: CompareFingerprints(entry.Source, observed.target), RollbackAvailable: entry.Source.Rows > 0}, nil
}

func (engine *AtomicExchangeCommitEngine) observeRolledBack(ctx context.Context, endpoint Endpoint, request ExchangeCommitRequest, entry LedgerEntry) (ExchangeCommitResult, error) {
	observed, err := engine.observe(ctx, endpoint, request, entry.Source)
	if err != nil {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("observe rolled-back runtime state: %w", err)
	}
	if observed.state != exchangeObservedPrepared {
		return ExchangeCommitResult{Entry: entry}, errors.New("ledger is rolled_back but runtime fingerprints do not prove the restored state")
	}
	return ExchangeCommitResult{Entry: entry, Verification: VerificationResult{Passed: true, Source: DataFingerprint{}, Target: observed.target}}, nil
}

func (engine *AtomicExchangeCommitEngine) exchange(ctx context.Context, endpoint Endpoint, database, targetTable, stagingTable string) error {
	query := fmt.Sprintf("EXCHANGE TABLES %s.%s AND %s.%s", quoteIdentifier(database), quoteIdentifier(targetTable), quoteIdentifier(database), quoteIdentifier(stagingTable))
	_, err := engine.client.Query(ctx, queryForEndpoint(endpoint, database, query, FormatTSVRaw))
	return err
}

type exchangeObservationState string

const (
	exchangeObservedCommitted exchangeObservationState = "committed"
	exchangeObservedPrepared  exchangeObservationState = "prepared"
	exchangeObservedUnknown   exchangeObservationState = "unknown"
)

type exchangeObservation struct {
	state   exchangeObservationState
	target  DataFingerprint
	staging DataFingerprint
}

func (engine *AtomicExchangeCommitEngine) observe(ctx context.Context, endpoint Endpoint, request ExchangeCommitRequest, source DataFingerprint) (exchangeObservation, error) {
	target, err := engine.verifier.Fingerprint(ctx, endpoint, request.Chunk.TargetDatabase, request.TargetTable, nil)
	if err != nil {
		return exchangeObservation{}, err
	}
	stagingTable := request.TargetTable
	stagingTable.Name = request.Chunk.StagingTable
	staging, err := engine.verifier.Fingerprint(ctx, endpoint, request.Chunk.TargetDatabase, stagingTable, nil)
	if err != nil {
		return exchangeObservation{}, err
	}
	observation := exchangeObservation{state: exchangeObservedUnknown, target: target, staging: staging}
	baseline := targetBaseline(request)
	if CompareFingerprints(source, target).Passed && matchesTargetBaseline(baseline, staging) {
		observation.state = exchangeObservedCommitted
	} else if matchesTargetBaseline(baseline, target) && CompareFingerprints(source, staging).Passed {
		observation.state = exchangeObservedPrepared
	}
	return observation, nil
}

func targetBaseline(request ExchangeCommitRequest) DataFingerprint {
	if request.TargetBaseline == nil {
		return DataFingerprint{}
	}
	return *request.TargetBaseline
}

func matchesTargetBaseline(expected, actual DataFingerprint) bool {
	if expected.Rows == 0 {
		return actual.Rows == 0
	}
	return CompareFingerprints(expected, actual).Passed
}

func (engine *AtomicExchangeCommitEngine) markCommitUnknown(entry LedgerEntry, cause error) (ExchangeCommitResult, error) {
	if entry.State != LedgerCommitUnknown {
		if err := moveLedger(&entry, LedgerCommitUnknown, engine.now()); err != nil {
			return ExchangeCommitResult{Entry: entry}, errors.Join(cause, err)
		}
	}
	entry.LastError = cause.Error()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ExchangeCommitResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist commit_unknown state: %w", err))
	}
	return ExchangeCommitResult{Entry: entry}, cause
}

func (engine *AtomicExchangeCommitEngine) markRollbackBlocked(entry LedgerEntry, cause error) (ExchangeCommitResult, error) {
	if err := moveLedger(&entry, LedgerRollbackBlocked, engine.now()); err != nil {
		return ExchangeCommitResult{Entry: entry}, errors.Join(cause, err)
	}
	entry.LastError = cause.Error()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ExchangeCommitResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist rollback_blocked state: %w", err))
	}
	return ExchangeCommitResult{Entry: entry}, cause
}

func (engine *AtomicExchangeCommitEngine) markRollbackFailed(entry LedgerEntry, cause error) (ExchangeCommitResult, error) {
	if entry.State != LedgerRollbackPending {
		if err := moveLedger(&entry, LedgerRollbackPending, engine.now()); err != nil {
			return ExchangeCommitResult{Entry: entry}, errors.Join(cause, err)
		}
	}
	if err := moveLedger(&entry, LedgerRollbackFailed, engine.now()); err != nil {
		return ExchangeCommitResult{Entry: entry}, errors.Join(cause, err)
	}
	entry.LastError = cause.Error()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ExchangeCommitResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist rollback_failed state: %w", err))
	}
	return ExchangeCommitResult{Entry: entry}, cause
}

func (engine *AtomicExchangeCommitEngine) markRollbackPending(entry LedgerEntry, cause error) (ExchangeCommitResult, error) {
	if entry.State != LedgerRollbackPending {
		return ExchangeCommitResult{Entry: entry}, fmt.Errorf("cannot preserve ambiguous rollback from %s: %w", entry.State, cause)
	}
	entry.Checkpoint, entry.LastError, entry.UpdatedAt = "rollback_observation_pending", cause.Error(), engine.now()
	if err := engine.ledger.Put(context.Background(), entry); err != nil {
		return ExchangeCommitResult{Entry: entry}, errors.Join(cause, fmt.Errorf("persist ambiguous rollback state: %w", err))
	}
	return ExchangeCommitResult{Entry: entry}, cause
}

func (engine *AtomicExchangeCommitEngine) classifyRollbackObservation(entry LedgerEntry, observed exchangeObservation, statementErr error, reconciled bool) (ExchangeCommitResult, error) {
	switch observed.state {
	case exchangeObservedPrepared:
		if err := moveLedger(&entry, LedgerRolledBack, engine.now()); err != nil {
			return ExchangeCommitResult{Entry: entry}, err
		}
		entry.Target, entry.Checkpoint, entry.LastError = observed.target, "exchange_rollback_verified", ""
		if reconciled {
			entry.Checkpoint = "rollback_reconciled_restored"
		}
		if err := engine.ledger.Put(context.Background(), entry); err != nil {
			return ExchangeCommitResult{Entry: entry}, fmt.Errorf("persist verified rollback state: %w", err)
		}
		return ExchangeCommitResult{Entry: entry, Verification: VerificationResult{Passed: true, Source: DataFingerprint{}, Target: observed.target}, RecoveredAmbiguous: reconciled || statementErr != nil}, nil
	case exchangeObservedCommitted:
		if err := moveLedger(&entry, LedgerCommitted, engine.now()); err != nil {
			return ExchangeCommitResult{Entry: entry}, err
		}
		entry.Target, entry.Checkpoint, entry.LastError = observed.target, "rollback_reconciled_not_applied", ""
		if err := engine.ledger.Put(context.Background(), entry); err != nil {
			return ExchangeCommitResult{Entry: entry}, fmt.Errorf("persist observed unapplied rollback state: %w", err)
		}
		cause := errors.New("rollback EXCHANGE is proven unapplied; a new explicit rollback attempt is required")
		if statementErr != nil {
			cause = fmt.Errorf("rollback EXCHANGE error: %v; %w", statementErr, cause)
		}
		return ExchangeCommitResult{Entry: entry, RecoveredAmbiguous: reconciled || statementErr != nil}, cause
	default:
		cause := errors.New("rollback observation contradicts both committed and restored fingerprints")
		if statementErr != nil {
			cause = fmt.Errorf("rollback EXCHANGE error: %v; %w", statementErr, cause)
		}
		return engine.markRollbackFailed(entry, cause)
	}
}
