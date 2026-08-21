package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV6OperationRunsToVersionedV7(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{
		schemaStatements, schemaV2Statements, schemaV3Statements, schemaV4Statements,
		schemaV5Statements, schemaV6Statements,
	} {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed v6 schema: %v", err)
			}
		}
	}
	now := time.Now().UTC().Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '6', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO operation_runs(
			id, idempotency_key, operation_id, operation_version, node_id,
			parameters_json, secret_refs_json, phase, created_at, updated_at
		) VALUES('v6-run', 'v6-idem', 'fixture.file.replace', '1.0.0', 'fixture-node',
			'{}', '[]', 'awaiting_confirmation', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v6 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var version int
	if err := store.db.QueryRowContext(ctx,
		`SELECT version FROM legacy_operation_runs_v7 WHERE id = 'v6-run'`).Scan(&version); err != nil {
		t.Fatalf("read archived v7 run: %v", err)
	}
	if version != 1 {
		t.Fatalf("archived run version=%d", version)
	}
}
