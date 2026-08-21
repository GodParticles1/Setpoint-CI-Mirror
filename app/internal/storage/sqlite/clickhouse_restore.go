package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"setpoint/internal/operation/clickhouse"
)

func (store *Store) PutRestore(ctx context.Context, record clickhouse.RestoreRecord) error {
	if err := clickhouse.ValidateRestoreRecord(record); err != nil {
		return fmt.Errorf("validate ClickHouse restore record: %w", err)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode ClickHouse restore record: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ClickHouse restore transaction: %w", err)
	}
	defer tx.Rollback()

	current, err := scanClickHouseRestore(tx.QueryRowContext(ctx,
		`SELECT record_json FROM clickhouse_restore_points WHERE run_id = ? AND database_name = ? AND table_name = ?`,
		record.Key.RunID, record.Key.Database, record.Key.Table))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read current ClickHouse restore record: %w", err)
	}
	if err == nil {
		if updateErr := validateClickHouseRestoreUpdate(current, record); updateErr != nil {
			return updateErr
		}
	} else if record.State != clickhouse.RestoreIntent {
		return errors.New("first ClickHouse restore record must persist intent before mutation")
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO clickhouse_restore_points(run_id, database_name, table_name, state, record_json, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, database_name, table_name) DO UPDATE SET
			state = excluded.state,
			record_json = excluded.record_json,
			updated_at = excluded.updated_at`,
		record.Key.RunID, record.Key.Database, record.Key.Table, string(record.State), string(payload), record.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("upsert ClickHouse restore record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ClickHouse restore transaction: %w", err)
	}
	return nil
}

func validateClickHouseRestoreUpdate(current, incoming clickhouse.RestoreRecord) error {
	if current.Key != incoming.Key || current.OwnershipToken != incoming.OwnershipToken || current.Target != incoming.Target || current.Restore.Table != incoming.Restore.Table || current.Restore.Database != incoming.Restore.Database {
		return errors.New("ClickHouse restore ownership or target identity changed")
	}
	if current.Baseline != incoming.Baseline || !reflect.DeepEqual(current.Partitions, incoming.Partitions) || !current.CreatedAt.Equal(incoming.CreatedAt) {
		return errors.New("ClickHouse restore frozen baseline changed")
	}
	if current.Restore.UUID != "" && current.Restore.UUID != incoming.Restore.UUID {
		return errors.New("ClickHouse restore object UUID changed")
	}
	if current.Restore.Engine != "" && current.Restore.Engine != incoming.Restore.Engine {
		return errors.New("ClickHouse restore object engine changed")
	}
	if current.Restore.SchemaFingerprint != "" && current.Restore.SchemaFingerprint != incoming.Restore.SchemaFingerprint {
		return errors.New("ClickHouse restore object schema changed")
	}
	if err := clickhouse.ValidateRestoreTransition(current.State, incoming.State); err != nil {
		return err
	}
	return nil
}

func (store *Store) GetRestore(ctx context.Context, key clickhouse.RestoreKey) (clickhouse.RestoreRecord, bool, error) {
	record, err := scanClickHouseRestore(store.db.QueryRowContext(ctx,
		`SELECT record_json FROM clickhouse_restore_points WHERE run_id = ? AND database_name = ? AND table_name = ?`,
		key.RunID, key.Database, key.Table))
	if errors.Is(err, sql.ErrNoRows) {
		return clickhouse.RestoreRecord{}, false, nil
	}
	if err != nil {
		return clickhouse.RestoreRecord{}, false, err
	}
	return record, true, nil
}

func (store *Store) ListRestores(ctx context.Context, runID string) ([]clickhouse.RestoreRecord, error) {
	rows, err := store.db.QueryContext(ctx,
		`SELECT record_json FROM clickhouse_restore_points WHERE run_id = ? ORDER BY database_name, table_name`, runID)
	if err != nil {
		return nil, fmt.Errorf("list ClickHouse restore records: %w", err)
	}
	defer rows.Close()
	records := make([]clickhouse.RestoreRecord, 0)
	for rows.Next() {
		record, err := scanClickHouseRestore(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ClickHouse restore records: %w", err)
	}
	return records, nil
}

func scanClickHouseRestore(source scanner) (clickhouse.RestoreRecord, error) {
	var payload string
	if err := source.Scan(&payload); err != nil {
		return clickhouse.RestoreRecord{}, err
	}
	var record clickhouse.RestoreRecord
	if err := json.Unmarshal([]byte(payload), &record); err != nil {
		return clickhouse.RestoreRecord{}, fmt.Errorf("decode ClickHouse restore record: %w", err)
	}
	if err := clickhouse.ValidateRestoreRecord(record); err != nil {
		return clickhouse.RestoreRecord{}, fmt.Errorf("validate persisted ClickHouse restore record: %w", err)
	}
	return record, nil
}

var _ clickhouse.RestoreStore = (*Store)(nil)
