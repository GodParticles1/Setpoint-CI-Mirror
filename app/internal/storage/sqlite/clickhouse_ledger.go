package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/operation/clickhouse"
)

const clickHouseLedgerColumns = `run_id, database_name, table_name, partition_key, chunk,
	strategy, state, attempt, checkpoint, staging_table,
	source_rows, source_bytes, source_hash_sum64, source_hash_xor64,
	target_rows, target_bytes, target_hash_sum64, target_hash_xor64,
	last_error, updated_at`

func (store *Store) Put(ctx context.Context, entry clickhouse.LedgerEntry) error {
	if err := clickhouse.ValidateLedgerEntry(entry); err != nil {
		return fmt.Errorf("validate ClickHouse ledger entry: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ClickHouse migration ledger transaction: %w", err)
	}
	defer tx.Rollback()

	current, err := scanClickHouseLedger(tx.QueryRowContext(ctx,
		`SELECT `+clickHouseLedgerColumns+` FROM clickhouse_migration_ledger
		 WHERE run_id = ? AND database_name = ? AND table_name = ? AND partition_key = ? AND chunk = ?`,
		entry.Key.RunID, entry.Key.Database, entry.Key.Table, entry.Key.Partition, entry.Key.Chunk))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read current ClickHouse migration ledger before update: %w", err)
	}
	if err == nil {
		if err := validateClickHouseLedgerUpdate(current, entry); err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO clickhouse_migration_ledger(
			run_id, database_name, table_name, partition_key, chunk,
			strategy, state, attempt, checkpoint, staging_table,
			source_rows, source_bytes, source_hash_sum64, source_hash_xor64,
			target_rows, target_bytes, target_hash_sum64, target_hash_xor64,
			last_error, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, database_name, table_name, partition_key, chunk) DO UPDATE SET
			strategy = excluded.strategy,
			state = excluded.state,
			attempt = excluded.attempt,
			checkpoint = excluded.checkpoint,
			staging_table = excluded.staging_table,
			source_rows = excluded.source_rows,
			source_bytes = excluded.source_bytes,
			source_hash_sum64 = excluded.source_hash_sum64,
			source_hash_xor64 = excluded.source_hash_xor64,
			target_rows = excluded.target_rows,
			target_bytes = excluded.target_bytes,
			target_hash_sum64 = excluded.target_hash_sum64,
			target_hash_xor64 = excluded.target_hash_xor64,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		entry.Key.RunID, entry.Key.Database, entry.Key.Table, entry.Key.Partition, entry.Key.Chunk,
		string(entry.Strategy), string(entry.State), entry.Attempt, entry.Checkpoint, entry.StagingTable,
		entry.Source.Rows, entry.Source.Bytes, entry.Source.HashSum64, entry.Source.HashXor64,
		entry.Target.Rows, entry.Target.Bytes, entry.Target.HashSum64, entry.Target.HashXor64,
		entry.LastError, entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert ClickHouse migration ledger: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ClickHouse migration ledger transaction: %w", err)
	}
	return nil
}

func validateClickHouseLedgerUpdate(current, incoming clickhouse.LedgerEntry) error {
	if current.Key != incoming.Key {
		return errors.New("ClickHouse ledger update identity changed")
	}
	if current.Strategy != incoming.Strategy {
		return fmt.Errorf("ClickHouse ledger strategy ownership changed from %q to %q", current.Strategy, incoming.Strategy)
	}
	if current.StagingTable != incoming.StagingTable {
		return fmt.Errorf("ClickHouse ledger staging ownership changed from %q to %q", current.StagingTable, incoming.StagingTable)
	}
	if incoming.Attempt < current.Attempt {
		return fmt.Errorf("ClickHouse ledger attempt regressed from %d to %d", current.Attempt, incoming.Attempt)
	}
	if current.State != incoming.State {
		if err := clickhouse.ValidateLedgerTransition(current.State, incoming.State); err != nil {
			return fmt.Errorf("invalid ClickHouse ledger persistence update: %w", err)
		}
	}
	return nil
}

func (store *Store) Get(ctx context.Context, key clickhouse.LedgerKey) (clickhouse.LedgerEntry, bool, error) {
	entry, err := scanClickHouseLedger(store.db.QueryRowContext(ctx,
		`SELECT `+clickHouseLedgerColumns+` FROM clickhouse_migration_ledger
		 WHERE run_id = ? AND database_name = ? AND table_name = ? AND partition_key = ? AND chunk = ?`,
		key.RunID, key.Database, key.Table, key.Partition, key.Chunk))
	if errors.Is(err, sql.ErrNoRows) {
		return clickhouse.LedgerEntry{}, false, nil
	}
	if err != nil {
		return clickhouse.LedgerEntry{}, false, err
	}
	return entry, true, nil
}

func (store *Store) ListRun(ctx context.Context, runID string) ([]clickhouse.LedgerEntry, error) {
	rows, err := store.db.QueryContext(ctx,
		`SELECT `+clickHouseLedgerColumns+` FROM clickhouse_migration_ledger
		 WHERE run_id = ? ORDER BY database_name, table_name, partition_key, chunk`, runID)
	if err != nil {
		return nil, fmt.Errorf("list ClickHouse migration ledger: %w", err)
	}
	defer rows.Close()
	entries := make([]clickhouse.LedgerEntry, 0)
	for rows.Next() {
		entry, err := scanClickHouseLedger(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ClickHouse migration ledger: %w", err)
	}
	return entries, nil
}

func scanClickHouseLedger(source scanner) (clickhouse.LedgerEntry, error) {
	var entry clickhouse.LedgerEntry
	var strategy, state, updatedAt string
	if err := source.Scan(
		&entry.Key.RunID, &entry.Key.Database, &entry.Key.Table, &entry.Key.Partition, &entry.Key.Chunk,
		&strategy, &state, &entry.Attempt, &entry.Checkpoint, &entry.StagingTable,
		&entry.Source.Rows, &entry.Source.Bytes, &entry.Source.HashSum64, &entry.Source.HashXor64,
		&entry.Target.Rows, &entry.Target.Bytes, &entry.Target.HashSum64, &entry.Target.HashXor64,
		&entry.LastError, &updatedAt,
	); err != nil {
		return clickhouse.LedgerEntry{}, err
	}
	entry.Strategy = clickhouse.StrategyID(strategy)
	entry.State = clickhouse.LedgerState(state)
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return clickhouse.LedgerEntry{}, fmt.Errorf("parse ClickHouse ledger update time: %w", err)
	}
	entry.UpdatedAt = parsed
	if err := clickhouse.ValidateLedgerEntry(entry); err != nil {
		return clickhouse.LedgerEntry{}, fmt.Errorf("validate persisted ClickHouse ledger entry: %w", err)
	}
	return entry, nil
}

var _ clickhouse.LedgerStore = (*Store)(nil)
