package clickhouse

import (
	"context"
	"errors"
	"fmt"
)

type NativeReconcileAction string

const (
	NativeReconcileNotNeeded           NativeReconcileAction = "not_needed"
	NativeReconcileReuseVerifiedStage  NativeReconcileAction = "reuse_verified_staging"
	NativeReconcileRestartCleanStaging NativeReconcileAction = "restart_clean_staging"
	NativeReconcileBlocked             NativeReconcileAction = "blocked"
)

type NativeReconcileResult struct {
	Action       NativeReconcileAction `json:"action"`
	Entry        LedgerEntry           `json:"entry"`
	Verification VerificationResult    `json:"verification"`
	Reason       string                `json:"reason,omitempty"`
}

// StagingInspector is intentionally separate from StagingController so an
// existing custom staging implementation cannot silently become replay-safe.
// Reconciliation of an interrupted transfer requires positive evidence about
// whether the run-owned staging object still exists.
type StagingInspector interface {
	Exists(context.Context, Endpoint, string, string) (bool, error)
}

// Reconcile inspects durable ledger state and the run-owned staging object
// before Execute is allowed to replay a Native stream. In particular, a stream
// that physically completed but crashed before its ledger transition is reused
// after fingerprint proof instead of being blindly written a second time.
func (engine *NativeExecutionEngine) Reconcile(ctx context.Context, request NativeChunkExecution, entry LedgerEntry) (NativeReconcileResult, error) {
	if entry.State != LedgerPlanned && entry.State != LedgerStaging && entry.State != LedgerTransferred && entry.State != LedgerVerified {
		return NativeReconcileResult{Action: NativeReconcileNotNeeded, Entry: entry}, nil
	}
	if err := validateLedgerOwnership(entry, request.Chunk); err != nil {
		return blockedReconcile(entry, err.Error())
	}

	inspector, ok := engine.staging.(StagingInspector)
	if !ok {
		return blockedReconcile(entry, "staging implementation cannot prove object existence; transfer replay is blocked")
	}

	pair, err := normalizePairParameters(request.Pair)
	if err != nil {
		return NativeReconcileResult{}, err
	}
	request.Pair = pair

	currentSource, err := fingerprintChunk(ctx, engine.verifier, pair.Source, request.Chunk.SourceDatabase, request.SourceTable, request.Chunk)
	if err != nil {
		return NativeReconcileResult{}, fmt.Errorf("reconcile source fingerprint: %w", err)
	}
	if ledgerSourceFingerprintRecorded(entry) && !CompareFingerprints(entry.Source, currentSource).Passed {
		return blockedReconcile(entry, "source fingerprint changed since the interrupted attempt; replay requires a new run")
	}
	entry.Source = currentSource

	exists, err := inspector.Exists(ctx, pair.Target, request.Chunk.TargetDatabase, request.Chunk.StagingTable)
	if err != nil {
		return NativeReconcileResult{}, fmt.Errorf("inspect run-owned staging during reconciliation: %w", err)
	}
	if !exists {
		if entry.State == LedgerTransferred || entry.State == LedgerVerified {
			return blockedReconcile(entry, "ledger records a completed transfer but the run-owned staging table is missing")
		}
		return NativeReconcileResult{
			Action: NativeReconcileRestartCleanStaging,
			Entry:  entry,
			Reason: "interrupted staging has no surviving staging object; a clean run-owned staging recreation may restart the transfer",
		}, nil
	}

	stagingTable := request.TargetTable
	stagingTable.Name = request.Chunk.StagingTable
	stagingFingerprint, err := fingerprintChunk(ctx, engine.verifier, pair.Target, request.Chunk.TargetDatabase, stagingTable, request.Chunk)
	if err != nil {
		return NativeReconcileResult{}, fmt.Errorf("reconcile staging fingerprint: %w", err)
	}
	verification := CompareFingerprints(currentSource, stagingFingerprint)
	if entry.State == LedgerPlanned {
		return NativeReconcileResult{
			Action:       NativeReconcileRestartCleanStaging,
			Entry:        entry,
			Verification: verification,
			Reason:       "staging recreation intent has an observed run-owned object; source is unchanged and recreation may resume without assuming transfer completion",
		}, nil
	}
	if verification.Passed {
		entry.Target = stagingFingerprint
		entry.LastError = ""
		entry.Checkpoint = "reconciled_existing_staging"
		if entry.State == LedgerStaging {
			if err := moveLedger(&entry, LedgerTransferred, engine.now()); err != nil {
				return NativeReconcileResult{}, err
			}
			if err := engine.ledger.Put(ctx, entry); err != nil {
				return NativeReconcileResult{}, fmt.Errorf("persist reconciled transferred staging: %w", err)
			}
		}
		if entry.State != LedgerVerified {
			if err := moveLedger(&entry, LedgerVerified, engine.now()); err != nil {
				return NativeReconcileResult{}, err
			}
		}
		if err := engine.ledger.Put(ctx, entry); err != nil {
			return NativeReconcileResult{}, fmt.Errorf("persist reconciled staging verification: %w", err)
		}
		return NativeReconcileResult{
			Action:       NativeReconcileReuseVerifiedStage,
			Entry:        entry,
			Verification: verification,
			Reason:       "existing run-owned staging exactly matches the current and recorded source fingerprint; Native replay skipped",
		}, nil
	}

	if entry.State == LedgerTransferred || entry.State == LedgerVerified {
		return blockedReconcile(entry, "ledger records a completed transfer but the surviving staging fingerprint does not match source; automatic replay is blocked")
	}
	return NativeReconcileResult{
		Action:       NativeReconcileRestartCleanStaging,
		Entry:        entry,
		Verification: verification,
		Reason:       "interrupted staging is partial or mismatched; it may be recreated because it is run-owned and the source fingerprint is unchanged",
	}, nil
}

func blockedReconcile(entry LedgerEntry, reason string) (NativeReconcileResult, error) {
	return NativeReconcileResult{Action: NativeReconcileBlocked, Entry: entry, Reason: reason}, errors.New(reason)
}

func fingerprintRecorded(value DataFingerprint) bool {
	return value.Rows != 0 || value.Bytes != 0 || value.HashSum64 != "" || value.HashXor64 != ""
}

func ledgerSourceFingerprintRecorded(entry LedgerEntry) bool {
	if fingerprintRecorded(entry.Source) || entry.Checkpoint != "" {
		return true
	}
	return entry.State != LedgerPlanned
}
