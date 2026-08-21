package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
)

func (store *Store) Append(ctx context.Context, entry operation.JournalEntry) error {
	_, err := store.SaveOperationExecutionCheckpoint(
		ctx,
		entry.RunID,
		entry.State,
		entry.Checkpoint,
		operationrun.ExecutionSnapshot{},
		nil,
		entry,
		entry.At,
	)
	return err
}

func (store *Store) List(ctx context.Context, runID string) ([]operation.JournalEntry, error) {
	var exists int
	if err := store.db.QueryRowContext(ctx, `SELECT 1 FROM operation_runs WHERE id = ?`, runID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read operation run for journal list: %w", err)
	}
	rows, err := store.db.QueryContext(ctx, `SELECT sequence, state, checkpoint, message, at, evidence_json FROM operation_journal WHERE run_id = ? ORDER BY sequence ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("list operation journal: %w", err)
	}
	defer rows.Close()
	entries := make([]operation.JournalEntry, 0)
	for rows.Next() {
		var entry operation.JournalEntry
		var state, at, evidence string
		entry.RunID = runID
		if err := rows.Scan(&entry.Sequence, &state, &entry.Checkpoint, &entry.Message, &at, &evidence); err != nil {
			return nil, fmt.Errorf("scan operation journal: %w", err)
		}
		entry.State = operation.State(state)
		parsed, err := parseTime(at, "operation journal")
		if err != nil {
			return nil, err
		}
		entry.At = parsed
		if err := json.Unmarshal([]byte(evidence), &entry.Evidence); err != nil {
			return nil, fmt.Errorf("decode operation journal evidence: %w", err)
		}
		if err := operation.ValidateJournalEntry(entry); err != nil {
			return nil, fmt.Errorf("validate persisted operation journal entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation journal: %w", err)
	}
	return entries, nil
}

func readOperationJournalEntry(ctx context.Context, transaction *sql.Tx, runID string, sequence int64) (operation.JournalEntry, bool, error) {
	var entry operation.JournalEntry
	var state, at, evidence string
	entry.RunID = runID
	err := transaction.QueryRowContext(ctx, `SELECT sequence, state, checkpoint, message, at, evidence_json FROM operation_journal WHERE run_id = ? AND sequence = ?`, runID, sequence).
		Scan(&entry.Sequence, &state, &entry.Checkpoint, &entry.Message, &at, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return operation.JournalEntry{}, false, nil
	}
	if err != nil {
		return operation.JournalEntry{}, false, fmt.Errorf("read operation journal entry: %w", err)
	}
	entry.State = operation.State(state)
	parsed, err := parseTime(at, "operation journal")
	if err != nil {
		return operation.JournalEntry{}, false, err
	}
	entry.At = parsed
	if err := json.Unmarshal([]byte(evidence), &entry.Evidence); err != nil {
		return operation.JournalEntry{}, false, fmt.Errorf("decode operation journal evidence: %w", err)
	}
	return entry, true, nil
}

var _ operation.Journal = (*Store)(nil)
