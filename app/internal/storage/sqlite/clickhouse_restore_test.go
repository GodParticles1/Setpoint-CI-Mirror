package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/operation/clickhouse"
)

func restoreRecordFixture(t *testing.T) clickhouse.RestoreRecord {
	t.Helper()
	ownershipToken := strings.Repeat("01", 16)
	restoreTable, err := clickhouse.BuildRestoreTableName("run-restore", "events", ownershipToken)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	return clickhouse.RestoreRecord{
		Key:            clickhouse.RestoreKey{RunID: "run-restore", Database: "db", Table: "events"},
		State:          clickhouse.RestoreIntent,
		OwnershipToken: ownershipToken,
		Target: clickhouse.RestoreObjectIdentity{
			Database: "db", Table: "events", UUID: "target-uuid", Engine: "MergeTree", SchemaFingerprint: "sha256:target",
		},
		Restore:    clickhouse.RestoreObjectIdentity{Database: "db", Table: restoreTable},
		Baseline:   clickhouse.DataFingerprint{Rows: 4, Bytes: 64, HashSum64: "40", HashXor64: "4"},
		Partitions: []clickhouse.Partition{{Partition: "all", Rows: 4, BytesOnDisk: 64, ActiveParts: 1}},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestClickHouseRestorePersistsLifecycleAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	record := restoreRecordFixture(t)
	if err := store.PutRestore(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State = clickhouse.RestoreCreating
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	if err := store.PutRestore(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State = clickhouse.RestoreReady
	record.Restore.UUID = "generated-restore-uuid"
	record.Restore.Engine = "MergeTree"
	record.Restore.SchemaFingerprint = record.Target.SchemaFingerprint
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	if err := store.PutRestore(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, ok, err := store.GetRestore(ctx, record.Key)
	if err != nil || !ok || got.State != clickhouse.RestoreReady || got.Restore.UUID != record.Restore.UUID || got.Baseline != record.Baseline {
		t.Fatalf("got=%#v ok=%v err=%v", got, ok, err)
	}
	listed, err := store.ListRestores(ctx, record.Key.RunID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
}

func TestClickHouseRestoreRejectsFrozenBaselineMutation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := restoreRecordFixture(t)
	if err := store.PutRestore(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Baseline.HashSum64 = "changed"
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	if err := store.PutRestore(ctx, record); err == nil || !strings.Contains(err.Error(), "frozen baseline") {
		t.Fatalf("frozen baseline mutation accepted: %v", err)
	}
}

func TestClickHouseRestoreRejectsOwnershipTokenMutation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := restoreRecordFixture(t)
	if err := store.PutRestore(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.OwnershipToken = strings.Repeat("10", 16)
	record.UpdatedAt = record.UpdatedAt.Add(time.Second)
	if err := store.PutRestore(ctx, record); err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("ownership token mutation accepted: %v", err)
	}
}

func TestClickHouseRestoreRejectsTargetWithoutUUID(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := restoreRecordFixture(t)
	record.Target.UUID = ""
	if err := store.PutRestore(ctx, record); err == nil || !strings.Contains(err.Error(), "target UUID") {
		t.Fatalf("target without UUID accepted: %v", err)
	}
}

func TestClickHouseRestoreRequiresIntentAsFirstDurableState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := restoreRecordFixture(t)
	record.State = clickhouse.RestoreReady
	record.Restore.UUID = "generated-restore-uuid"
	record.Restore.Engine = "MergeTree"
	record.Restore.SchemaFingerprint = record.Target.SchemaFingerprint
	if err := store.PutRestore(ctx, record); err == nil || !strings.Contains(err.Error(), "must persist intent") {
		t.Fatalf("non-intent initial state accepted: %v", err)
	}
	if _, ok, err := store.GetRestore(ctx, record.Key); err != nil || ok {
		t.Fatalf("initial state leaked into storage: ok=%v err=%v", ok, err)
	}
}

func TestClickHouseRestoreReadRejectsCorruptPersistedRecord(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	record := restoreRecordFixture(t)
	if err := store.PutRestore(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State = clickhouse.RestoreState("corrupt")
	record.Restore.UUID = "generated-restore-uuid"
	record.Restore.Engine = "MergeTree"
	record.Restore.SchemaFingerprint = record.Target.SchemaFingerprint
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE clickhouse_restore_points SET record_json = ? WHERE run_id = ?`, string(payload), record.Key.RunID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.GetRestore(ctx, record.Key); err == nil || ok || !strings.Contains(err.Error(), "unsupported ClickHouse restore state") {
		t.Fatalf("get ok=%v err=%v", ok, err)
	}
}

func TestOpenMigratesSchemaV8ToV9ClickHouseRestore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{schemaStatements, schemaV2Statements, schemaV3Statements, schemaV4Statements, schemaV5Statements, schemaV6Statements, schemaV7Statements, schemaV8Statements} {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed v8 schema: %v", err)
			}
		}
	}
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '8', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES('migration_fixture', 'preserved', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, table, preserved, integrity string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='clickhouse_restore_points'`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='migration_fixture'`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	var foreignKeyViolations int
	rows, err := store.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		foreignKeyViolations++
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || table != "clickhouse_restore_points" || preserved != "preserved" || integrity != "ok" || foreignKeyViolations != 0 {
		t.Fatalf("version=%q table=%q preserved=%q integrity=%q foreign_key_violations=%d", version, table, preserved, integrity, foreignKeyViolations)
	}
}
