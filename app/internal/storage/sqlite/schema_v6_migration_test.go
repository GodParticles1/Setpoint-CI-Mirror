package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV10ThroughExecutionSnapshotWithoutChangingPlanningRuns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{schemaStatements, schemaV2Statements, schemaV3Statements} {
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed base schema: %v", err)
			}
		}
	}
	if err := migrateSchemaV4(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	if err := migrateSchemaV5(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{schemaV6Statements, schemaV7Statements, schemaV8Statements, schemaV9Statements} {
		for _, statement := range statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed integrated schema: %v", err)
			}
		}
	}
	if err := migrateSchemaV10(ctx, transaction); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := transaction.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '10', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO nodes(id, hostname, os, os_version, arch, agent_version, registered_at, last_seen_at, updated_at, reported_address, tags_json, notes) VALUES('node-v5','node','linux','test','amd64','test',?,?,?, '', '[]', '')`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,attempt,created_at,updated_at,kind,operation_id,operation_version,capability_digest,targets_json,secret_refs_json) VALUES('task-v5','task-idem-v5','node-v5',NULL,'{}','succeeded',1,?,?,'OperationPlanningTask','operation.test','1.0.0','sha256:cap','[{"kind":"node","node_id":"node-v5"}]','[]')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO operation_runs(id,idempotency_key,operation_id,operation_version,capability_digest,node_id,targets_json,parameters_json,secret_refs_json,state,checkpoint,task_id,plan_digest,created_at,updated_at) VALUES('run-v5','run-idem-v5','operation.test','1.0.0','sha256:cap','node-v5','[{"kind":"node","node_id":"node-v5"}]','{}','[]','awaiting_confirmation','plan_ready','task-v5','sha256:plan',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := open(ctx, path, func() time.Time { return time.Date(2026, 8, 17, 5, 1, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("migrate v10 store: %v", err)
	}
	defer store.Close()

	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("schema_version=%q", version)
	}
	columns, err := store.db.QueryContext(ctx, `PRAGMA table_info(operation_runs)`)
	if err != nil {
		t.Fatal(err)
	}
	foundExecution := false
	for columns.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			columns.Close()
			t.Fatal(err)
		}
		if name == "execution_json" {
			foundExecution = true
		}
	}
	if err := columns.Close(); err != nil {
		t.Fatal(err)
	}
	if !foundExecution {
		t.Fatal("operation_runs.execution_json migration missing")
	}
	run, err := store.GetOperationRun(ctx, "run-v5")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status.State != "awaiting_confirmation" || run.PlanDigest != "sha256:plan" || run.Execution != nil {
		t.Fatalf("migrated planning run changed: %#v", run)
	}
	rows, err := store.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("current migration left a foreign key violation")
	}
}
