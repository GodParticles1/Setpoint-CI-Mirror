package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEveryHistoricalSchemaMigratesToLatestAndReopens(t *testing.T) {
	for version := 1; version < 15; version++ {
		version := version
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "setpoint.db")
			seedHistoricalSchema(t, ctx, path, version)

			store, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("migrate v%d to latest: %v", version, err)
			}
			assertLatestSchemaHealth(t, ctx, store)
			if err := store.Close(); err != nil {
				t.Fatalf("close migrated v%d store: %v", version, err)
			}

			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("reopen migrated v%d store: %v", version, err)
			}
			defer reopened.Close()
			assertLatestSchemaHealth(t, ctx, reopened)
		})
	}
}

func seedHistoricalSchema(t *testing.T, ctx context.Context, path string, target int) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	applyStatements := func(version int, statements []string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed schema v%d: %v", version, err)
			}
		}
	}
	applyMigration := func(version int, migrate func(context.Context, *sql.Tx) error) {
		t.Helper()
		if err := migrate(ctx, transaction); err != nil {
			t.Fatalf("seed schema v%d: %v", version, err)
		}
	}
	recordVersion := func(version int) {
		t.Helper()
		if _, err := transaction.ExecContext(ctx, `UPDATE settings SET value = ?, updated_at = ? WHERE key = 'schema_version'`, fmt.Sprint(version), time.Date(2026, 8, 17, 0, 0, version, 0, time.UTC).Format(timeFormat)); err != nil {
			t.Fatalf("record schema v%d: %v", version, err)
		}
	}
	applyStatements(1, schemaStatements)
	if _, err := transaction.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '1', ?)`, time.Date(2026, 8, 17, 0, 0, 1, 0, time.UTC).Format(timeFormat)); err != nil {
		t.Fatal(err)
	}
	if target >= 2 {
		applyStatements(2, schemaV2Statements)
		recordVersion(2)
	}
	if target >= 3 {
		applyStatements(3, schemaV3Statements)
		recordVersion(3)
	}
	if target >= 4 {
		applyMigration(4, migrateSchemaV4)
		recordVersion(4)
	}
	if target >= 5 {
		applyMigration(5, migrateSchemaV5)
		recordVersion(5)
	}
	if target >= 6 {
		applyStatements(6, schemaV6Statements)
		recordVersion(6)
	}
	if target >= 7 {
		applyStatements(7, schemaV7Statements)
		recordVersion(7)
	}
	if target >= 8 {
		applyStatements(8, schemaV8Statements)
		recordVersion(8)
	}
	if target >= 9 {
		applyStatements(9, schemaV9Statements)
		recordVersion(9)
	}
	if target >= 10 {
		applyMigration(10, migrateSchemaV10)
		recordVersion(10)
	}
	if target >= 11 {
		applyStatements(11, schemaV11Statements)
		recordVersion(11)
	}
	if target >= 12 {
		applyStatements(12, schemaV12Statements)
		recordVersion(12)
	}
	if target >= 13 {
		applyStatements(13, schemaV13Statements)
		recordVersion(13)
	}
	if target >= 14 {
		applyStatements(14, schemaV14Statements)
		recordVersion(14)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertLatestSchemaHealth(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version=%q, want %q", version, schemaVersion)
	}
	var integrity string
	if err := store.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check=%q", integrity)
	}
	rows, err := store.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation")
	}
}
