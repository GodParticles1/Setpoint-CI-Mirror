package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
)

// OperationRuntimeSnapshot is a read-only reconstruction of the durable facts
// required to decide what is safe after a Server restart. It deliberately does
// not dispatch work or authorize Product Apply.
type OperationRuntimeSnapshot struct {
	Run                        operationrun.Resource   `json:"run"`
	JournalTail                *operation.JournalEntry `json:"journal_tail,omitempty"`
	NextJournalSequence        int64                   `json:"next_journal_sequence"`
	Lease                      *operation.LockLease    `json:"lease,omitempty"`
	LeaseActive                bool                    `json:"lease_active"`
	WriteBlockedUntilReconcile bool                    `json:"write_blocked_until_reconcile"`
}

func (store *Store) LoadOperationRuntimeSnapshot(ctx context.Context, runID string, now time.Time) (OperationRuntimeSnapshot, error) {
	run, err := store.GetOperationRun(ctx, runID)
	if err != nil {
		return OperationRuntimeSnapshot{}, err
	}
	if !operation.ValidState(run.Status.State) {
		return OperationRuntimeSnapshot{}, fmt.Errorf("persisted operation run has invalid state %q", run.Status.State)
	}
	journal, err := store.List(ctx, runID)
	if err != nil {
		return OperationRuntimeSnapshot{}, err
	}
	snapshot := OperationRuntimeSnapshot{Run: run, NextJournalSequence: 1}
	if len(journal) > 0 {
		tail := journal[len(journal)-1]
		if tail.State != run.Status.State || tail.Checkpoint != run.Status.Checkpoint {
			return OperationRuntimeSnapshot{}, fmt.Errorf("operation run state/checkpoint diverges from durable journal tail: run=%s/%s journal=%s/%s", run.Status.State, run.Status.Checkpoint, tail.State, tail.Checkpoint)
		}
		copy := tail
		snapshot.JournalTail = &copy
		snapshot.NextJournalSequence = tail.Sequence + 1
	} else if executionStateRequiresJournal(run.Status.State) {
		return OperationRuntimeSnapshot{}, fmt.Errorf("operation run %s in execution state %s has no durable journal", runID, run.Status.State)
	}

	lease, found, err := store.loadOperationLeaseByOwner(ctx, runID)
	if err != nil {
		return OperationRuntimeSnapshot{}, err
	}
	if found {
		copy := lease
		snapshot.Lease = &copy
		snapshot.LeaseActive = operation.ValidateLease(lease, now) == nil
	}
	snapshot.WriteBlockedUntilReconcile = operationStateRequiresReconcile(run.Status.State)
	return snapshot, nil
}

func (store *Store) loadOperationLeaseByOwner(ctx context.Context, ownerID string) (operation.LockLease, bool, error) {
	var lease operation.LockLease
	var resourcesJSON, acquiredAt, expiresAt string
	err := store.db.QueryRowContext(ctx, `SELECT id, owner_id, resources_json, acquired_at, expires_at FROM operation_leases WHERE owner_id = ?`, ownerID).
		Scan(&lease.ID, &lease.OwnerID, &resourcesJSON, &acquiredAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.LockLease{}, false, nil
	}
	if err != nil {
		return operation.LockLease{}, false, fmt.Errorf("read operation runtime lease: %w", err)
	}
	lease, err = decodeOperationLease(lease, resourcesJSON, acquiredAt, expiresAt)
	if err != nil {
		return operation.LockLease{}, false, err
	}
	return lease, true, nil
}

func executionStateRequiresJournal(state operation.State) bool {
	switch state {
	case operation.StateQueued,
		operation.StateAcquiringLock,
		operation.StateCreatingRestorePoint,
		operation.StateRunning,
		operation.StateVerifying,
		operation.StateSucceeded,
		operation.StateFailed,
		operation.StateRollingBack,
		operation.StateRolledBack,
		operation.StateRollbackFailed,
		operation.StateInterrupted:
		return true
	default:
		return false
	}
}

func operationStateRequiresReconcile(state operation.State) bool {
	switch state {
	case operation.StateCreatingRestorePoint,
		operation.StateRunning,
		operation.StateVerifying,
		operation.StateFailed,
		operation.StateRollingBack,
		operation.StateInterrupted:
		return true
	default:
		return false
	}
}
