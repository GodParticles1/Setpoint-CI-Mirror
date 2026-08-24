package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

// SaveOperationExecutionCheckpoint atomically advances the durable OperationRun
// snapshot and appends the matching lifecycle journal fact. It is the storage
// boundary for future task transport integration; it does not dispatch or
// authorize Product Apply by itself.
func (store *Store) SaveOperationExecutionCheckpoint(
	ctx context.Context,
	runID string,
	state operation.State,
	checkpoint string,
	snapshot operationrun.ExecutionSnapshot,
	recovery *operationrun.Recovery,
	journal operation.JournalEntry,
	at time.Time,
) (operationrun.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operationrun.Resource{}, fmt.Errorf("begin atomic operation checkpoint: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	if _, err := saveOperationExecutionCheckpointTx(ctx, transaction, runID, state, checkpoint, snapshot, recovery, journal, at); err != nil {
		return operationrun.Resource{}, err
	}
	if err := transaction.Commit(); err != nil {
		return operationrun.Resource{}, fmt.Errorf("commit atomic operation checkpoint: %w", err)
	}
	return store.GetOperationRun(ctx, runID)
}

func saveOperationExecutionCheckpointTx(
	ctx context.Context,
	transaction *sql.Tx,
	runID string,
	state operation.State,
	checkpoint string,
	snapshot operationrun.ExecutionSnapshot,
	recovery *operationrun.Recovery,
	journal operation.JournalEntry,
	at time.Time,
) (operationrun.Resource, error) {
	if journal.RunID != runID || journal.State != state || journal.Checkpoint != checkpoint || !journal.At.Equal(at) {
		return operationrun.Resource{}, errors.New("operation checkpoint journal does not match the durable run update")
	}
	if err := operation.ValidateJournalEntry(journal); err != nil {
		return operationrun.Resource{}, err
	}

	current, err := scanOperationRun(transaction.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return operationrun.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return operationrun.Resource{}, err
	}

	// A retry may arrive after the run has already advanced beyond this journal
	// sequence. If the durable journal fact is equivalent in business terms,
	// treat it as acknowledged and never move the run backward.
	existing, found, err := readOperationJournalEntry(ctx, transaction, runID, journal.Sequence)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if found {
		if !sameOperationJournalEntry(existing, journal) {
			return operationrun.Resource{}, fmt.Errorf("operation journal sequence %d already contains different durable facts", journal.Sequence)
		}
		return current, nil
	}

	if !operation.ValidState(state) {
		return operationrun.Resource{}, fmt.Errorf("invalid operation execution state %q", state)
	}
	if state != current.Status.State && !operation.CanTransition(current.Status.State, state) {
		return operationrun.Resource{}, fmt.Errorf("%w: operation run cannot advance from %s to %s", task.ErrInvalidTransition, current.Status.State, state)
	}
	merged, err := mergeOperationExecutionSnapshot(current.Execution, snapshot)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if err := validateOperationExecutionSnapshot(merged, at); err != nil {
		return operationrun.Resource{}, err
	}
	executionJSON, err := nullableJSON(merged)
	if err != nil {
		return operationrun.Resource{}, err
	}
	mergedRecovery := current.Status.Recovery
	if recovery != nil {
		copy := *recovery
		mergedRecovery = &copy
	}
	recoveryJSON, err := nullableJSON(mergedRecovery)
	if err != nil {
		return operationrun.Resource{}, err
	}

	if err := appendOperationJournalEntryTx(ctx, transaction, journal); err != nil {
		return operationrun.Resource{}, err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE operation_runs SET state = ?, checkpoint = ?, execution_json = ?, recovery_json = ?, updated_at = ? WHERE id = ?`,
		state, checkpoint, executionJSON, recoveryJSON, formatTime(at), runID); err != nil {
		return operationrun.Resource{}, fmt.Errorf("update atomic operation checkpoint: %w", err)
	}
	updated := current
	updated.Status.State = state
	updated.Status.Checkpoint = checkpoint
	updated.Status.UpdatedAt = at
	updated.Status.Recovery = mergedRecovery
	updated.Execution = merged
	return updated, nil
}

func appendOperationJournalEntryTx(ctx context.Context, transaction *sql.Tx, entry operation.JournalEntry) error {
	existing, found, err := readOperationJournalEntry(ctx, transaction, entry.RunID, entry.Sequence)
	if err != nil {
		return err
	}
	if found {
		if sameOperationJournalEntry(existing, entry) {
			return nil
		}
		return fmt.Errorf("operation journal sequence %d already contains different durable facts", entry.Sequence)
	}
	var previousSequence int64
	var previousState string
	err = transaction.QueryRowContext(ctx, `SELECT sequence, state FROM operation_journal WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`, entry.RunID).Scan(&previousSequence, &previousState)
	if errors.Is(err, sql.ErrNoRows) {
		if entry.Sequence != 1 {
			return fmt.Errorf("operation journal must start at sequence 1, got %d", entry.Sequence)
		}
	} else if err != nil {
		return fmt.Errorf("read operation journal tail: %w", err)
	} else {
		if entry.Sequence != previousSequence+1 {
			return fmt.Errorf("operation journal sequence must advance from %d to %d, got %d", previousSequence, previousSequence+1, entry.Sequence)
		}
		from := operation.State(previousState)
		if entry.State != from && !operation.CanTransition(from, entry.State) {
			return fmt.Errorf("operation journal state cannot advance from %s to %s", from, entry.State)
		}
	}
	evidence, err := json.Marshal(entry.Evidence)
	if err != nil {
		return fmt.Errorf("encode operation journal evidence: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operation_journal(run_id, sequence, state, checkpoint, message, at, evidence_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		entry.RunID, entry.Sequence, entry.State, entry.Checkpoint, entry.Message, formatTime(entry.At), string(evidence)); err != nil {
		return fmt.Errorf("insert operation journal entry: %w", err)
	}
	return nil
}

func sameOperationJournalEntry(left, right operation.JournalEntry) bool {
	return left.RunID == right.RunID && left.Sequence == right.Sequence && left.State == right.State &&
		left.Checkpoint == right.Checkpoint && left.Message == right.Message && left.At.Equal(right.At) &&
		reflect.DeepEqual(left.Evidence, right.Evidence)
}
