package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

// ContinueOperationRun persists a Server-selected lifecycle transition and its
// single next bounded task in one transaction. The caller owns the next-action
// decision; storage only validates and durably records that decision.
func (store *Store) ContinueOperationRun(
	ctx context.Context,
	runID string,
	completedTaskID string,
	state operation.State,
	checkpoint string,
	nextTask task.Resource,
	journal operation.JournalEntry,
	at time.Time,
) (operationrun.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operationrun.Resource{}, fmt.Errorf("begin operation continuation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	current, err := scanOperationRun(transaction.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return operationrun.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return operationrun.Resource{}, err
	}
	if current.Status.State == state && current.Status.Checkpoint == checkpoint && current.Status.TaskID == nextTask.Metadata.ID {
		stored, err := scanTask(transaction.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, nextTask.Metadata.ID))
		if err != nil {
			return operationrun.Resource{}, fmt.Errorf("continued operation task is missing: %w", err)
		}
		if stored.Metadata.IdempotencyKey != nextTask.Metadata.IdempotencyKey || !sameTaskSpec(stored, nextTask) || !reflect.DeepEqual(stored.Spec.OperationExecution, nextTask.Spec.OperationExecution) {
			return operationrun.Resource{}, fmt.Errorf("%w: continued operation task differs from retry", task.ErrResultConflict)
		}
		persistedJournal, found, err := readOperationJournalEntry(ctx, transaction, runID, journal.Sequence)
		if err != nil {
			return operationrun.Resource{}, err
		}
		if !found || !sameOperationJournalEntry(persistedJournal, journal) {
			return operationrun.Resource{}, fmt.Errorf("%w: continued operation journal differs from retry", task.ErrResultConflict)
		}
		return current, nil
	}
	if current.Status.TaskID != completedTaskID {
		return operationrun.Resource{}, fmt.Errorf("%w: operation run task changed from %s to %s", task.ErrResultConflict, completedTaskID, current.Status.TaskID)
	}
	if !operation.ValidState(state) || state != current.Status.State && !operation.CanTransition(current.Status.State, state) {
		return operationrun.Resource{}, fmt.Errorf("%w: operation run cannot continue from %s to %s", task.ErrInvalidTransition, current.Status.State, state)
	}
	if nextTask.Kind != task.KindOperationExecutionTask || nextTask.Spec.OperationExecution == nil {
		return operationrun.Resource{}, errors.New("operation continuation requires one OperationExecutionTask")
	}
	if nextTask.Spec.OperationExecution.RunID != runID || nextTask.Spec.OperationExecution.OperationID != current.Spec.OperationID {
		return operationrun.Resource{}, errors.New("operation continuation task does not match the durable run")
	}
	if journal.RunID != runID || journal.State != state || journal.Checkpoint != checkpoint || !journal.At.Equal(at) {
		return operationrun.Resource{}, errors.New("operation continuation journal does not match the Server-selected transition")
	}
	if err := operation.ValidateJournalEntry(journal); err != nil {
		return operationrun.Resource{}, err
	}
	if err := appendOperationJournalEntryTx(ctx, transaction, journal); err != nil {
		return operationrun.Resource{}, err
	}
	if err := insertTask(ctx, transaction, nextTask); err != nil {
		return operationrun.Resource{}, err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE operation_runs SET state = ?, checkpoint = ?, task_id = ?, updated_at = ? WHERE id = ?`,
		state, checkpoint, nextTask.Metadata.ID, formatTime(at), runID); err != nil {
		return operationrun.Resource{}, fmt.Errorf("persist operation continuation: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return operationrun.Resource{}, fmt.Errorf("commit operation continuation: %w", err)
	}
	return store.GetOperationRun(ctx, runID)
}
