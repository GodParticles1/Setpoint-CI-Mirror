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

func TestPhase3ParentV9MigratesToIntegratedSchemaWithoutLosingDurableState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	seedPhase3ParentV9(t, ctx, path)

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open Phase 3 parent v9 database: %v", err)
	}
	assertLatestSchemaHealth(t, ctx, store)
	assertPhase3ParentStatePreserved(t, ctx, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated Phase 3 parent database: %v", err)
	}
	defer reopened.Close()
	assertLatestSchemaHealth(t, ctx, reopened)
	assertPhase3ParentStatePreserved(t, ctx, reopened.db)
}

func TestEveryPhase3HistoricalSchemaMigratesToIntegratedLatest(t *testing.T) {
	for version := 4; version <= 9; version++ {
		version := version
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "setpoint.db")
			seedPhase3ParentSchema(t, ctx, path, version)
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("migrate Phase 3 schema v%d: %v", version, err)
			}
			defer store.Close()
			assertLatestSchemaHealth(t, ctx, store)
		})
	}
}

func seedPhase3ParentV9(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	seedPhase3ParentSchema(t, ctx, path, 9)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC).Format(timeFormat)
	statements := []string{
		`INSERT INTO sites(id,idempotency_key,name,description,created_at,updated_at) VALUES('site-parent','site-idem','parent','phase3','` + now + `','` + now + `')`,
		`INSERT INTO nodes(id,hostname,os,os_version,arch,agent_version,registered_at,last_seen_at,updated_at,site_id) VALUES('node-parent','node','linux','test','amd64','test','` + now + `','` + now + `','` + now + `','site-parent')`,
		`INSERT INTO plugins(id,name,version,description,mode,risk,impact,supported_systems,parameters,updated_at,category,checks_json) VALUES('linux.parent','Parent','1.0.0','parent','read_only','low','none','["linux"]','[]','` + now + `','linux','[{"id":"parent.check","name":"Parent check","recommended_value":"safe","source_refs":["integration:parent"]}]')`,
		`INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,attempt,created_at,updated_at,kind) VALUES('check-task','check-idem','node-parent','linux.parent','{}','succeeded',1,'` + now + `','` + now + `','ReadOnlyCheckTask')`,
		`INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,attempt,created_at,updated_at,kind,operation_id,operation_version,capability_digest,targets_json,secret_refs_json) VALUES('operation-task','operation-idem','node-parent',NULL,'{}','succeeded',1,'` + now + `','` + now + `','OperationPlanningTask','operation.clickhouse.online_migration','1.0.0','sha256:cap','[{"kind":"node","node_id":"node-parent"}]','[]')`,
		`INSERT INTO task_results(task_id,result_json,result_digest,reported_at) VALUES('check-task','{"plugin_id":"linux.parent"}',X'0102','` + now + `')`,
		`INSERT INTO task_events(task_id,phase,event_type,created_at,details_json) VALUES('check-task','succeeded','completed','` + now + `','{}')`,
		`INSERT INTO check_runs(id,idempotency_key,name,node_ids_json,plugin_ids_json,parameters_json,created_at) VALUES('check-run','check-run-idem','parent','["node-parent"]','["linux.parent"]','{}','` + now + `')`,
		`INSERT INTO check_run_tasks(run_id,task_id) VALUES('check-run','check-task')`,
		`INSERT INTO operation_runs(id,idempotency_key,operation_id,operation_version,capability_digest,node_id,targets_json,parameters_json,secret_refs_json,state,checkpoint,task_id,plan_digest,created_at,updated_at,execution_json) VALUES('operation-run','operation-run-idem','operation.clickhouse.online_migration','1.0.0','sha256:cap','node-parent','[{"kind":"node","node_id":"node-parent"}]','{}','[]','awaiting_confirmation','plan_ready','operation-task','sha256:plan','` + now + `','` + now + `','{"schema_version":"clickhouse.execution.v1"}')`,
		`INSERT INTO operation_journal(run_id,sequence,state,checkpoint,message,at,evidence_json) VALUES('operation-run',1,'awaiting_confirmation','plan_ready','preserved journal','` + now + `','[]')`,
		`INSERT INTO operation_leases(id,owner_id,resources_json,acquired_at,expires_at) VALUES('lease-parent','operation-run','["node:node-parent"]','` + now + `','` + now + `')`,
		`INSERT INTO operation_lock_resources(resource_key,lease_id) VALUES('node:node-parent','lease-parent')`,
		`INSERT INTO clickhouse_migration_ledger(run_id,database_name,table_name,chunk,strategy,state,attempt,updated_at) VALUES('operation-run','db','events',0,'native','planned',1,'` + now + `')`,
		`INSERT INTO clickhouse_restore_points(run_id,database_name,table_name,state,record_json,updated_at) VALUES('operation-run','db','events','ready','{"preserved":true}','` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed Phase 3 parent data: %v", err)
		}
	}
}

func seedPhase3ParentSchema(t *testing.T, ctx context.Context, path string, version int) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	versions := []struct {
		minimum    int
		statements []string
	}{
		{1, schemaStatements},
		{2, schemaV2Statements},
		{3, schemaV3Statements},
		{4, schemaV9Statements},
		{5, phase3ParentV5Statements},
		{6, schemaV11Statements},
		{7, schemaV12Statements},
		{8, schemaV13Statements},
		{9, schemaV14Statements},
	}
	for _, migration := range versions {
		if version < migration.minimum {
			continue
		}
		for _, statement := range migration.statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed Phase 3 schema v%d: %v", version, err)
			}
		}
	}
	now := time.Date(2026, 8, 17, 8, 0, version, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key,value,updated_at) VALUES('schema_version',?,?)`, version, now); err != nil {
		t.Fatalf("record Phase 3 schema v%d: %v", version, err)
	}
}

func assertPhase3ParentStatePreserved(t *testing.T, ctx context.Context, database *sql.DB) {
	t.Helper()
	var contract, digest string
	if err := database.QueryRowContext(ctx, `SELECT execution_contract_json,execution_contract_digest FROM tasks WHERE id='check-task'`).Scan(&contract, &digest); err != nil {
		t.Fatal(err)
	}
	if contract == "" || digest == "" {
		t.Fatalf("Check contract was not frozen: contract=%q digest=%q", contract, digest)
	}
	var operationID, planDigest, execution string
	if err := database.QueryRowContext(ctx, `SELECT operation_id,plan_digest,execution_json FROM operation_runs WHERE id='operation-run'`).Scan(&operationID, &planDigest, &execution); err != nil {
		t.Fatal(err)
	}
	if operationID != "operation.clickhouse.online_migration" || planDigest != "sha256:plan" || execution == "" {
		t.Fatalf("Operation run changed: operation=%q plan=%q execution=%q", operationID, planDigest, execution)
	}
	checks := map[string]string{
		`SELECT message FROM operation_journal WHERE run_id='operation-run' AND sequence=1`:                 "preserved journal",
		`SELECT lease_id FROM operation_lock_resources WHERE resource_key='node:node-parent'`:               "lease-parent",
		`SELECT state FROM clickhouse_migration_ledger WHERE run_id='operation-run' AND database_name='db'`: "planned",
		`SELECT state FROM clickhouse_restore_points WHERE run_id='operation-run' AND database_name='db'`:   "ready",
		`SELECT trusted_executable_roots_json FROM sites WHERE id='site-parent'`:                            "[]",
		`SELECT trusted_executable_roots_json FROM nodes WHERE id='node-parent'`:                            "[]",
	}
	for query, want := range checks {
		var got string
		if err := database.QueryRowContext(ctx, query).Scan(&got); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if got != want {
			t.Fatalf("query %q got %q want %q", query, got, want)
		}
	}
	var definitionIDs string
	if err := database.QueryRowContext(ctx, `SELECT definition_ids_json FROM check_runs WHERE id='check-run'`).Scan(&definitionIDs); err != nil {
		t.Fatal(err)
	}
	if definitionIDs != `["parent.check"]` {
		t.Fatalf("definition_ids_json=%s", definitionIDs)
	}
}

var phase3ParentV5Statements = []string{
	`DROP TABLE check_run_tasks`,
	`DROP TABLE task_results`,
	`DROP TABLE task_events`,
	`DROP TABLE tasks`,
	`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		node_id TEXT NOT NULL,
		plugin_id TEXT,
		parameters_json TEXT NOT NULL,
		phase TEXT NOT NULL CHECK(phase IN ('pending','claimed','running','cancel_requested','canceled','succeeded','failed')),
		claim_id TEXT,
		attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt >= 0),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		claimed_at TEXT,
		acknowledged_at TEXT,
		cancel_requested_at TEXT,
		completed_at TEXT,
		last_error_code TEXT,
		last_error_message TEXT,
		kind TEXT NOT NULL DEFAULT 'ReadOnlyCheckTask' CHECK(kind IN ('ReadOnlyCheckTask','OperationPlanningTask')),
		operation_id TEXT NOT NULL DEFAULT '',
		operation_version TEXT NOT NULL DEFAULT '',
		capability_digest TEXT NOT NULL DEFAULT '',
		targets_json TEXT NOT NULL DEFAULT '[]',
		secret_refs_json TEXT NOT NULL DEFAULT '[]',
		CHECK((kind = 'ReadOnlyCheckTask' AND plugin_id IS NOT NULL AND plugin_id <> '' AND operation_id = '') OR
			(kind = 'OperationPlanningTask' AND plugin_id IS NULL AND operation_id <> '' AND operation_version <> '' AND capability_digest <> '')),
		FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE RESTRICT,
		FOREIGN KEY(plugin_id) REFERENCES plugins(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE task_results (task_id TEXT PRIMARY KEY,result_json TEXT NOT NULL,result_digest BLOB NOT NULL,reported_at TEXT NOT NULL,FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT)`,
	`CREATE TABLE task_events (id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT NOT NULL,phase TEXT NOT NULL,event_type TEXT NOT NULL,created_at TEXT NOT NULL,details_json TEXT NOT NULL,FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT)`,
	`CREATE TABLE check_run_tasks (run_id TEXT NOT NULL,task_id TEXT NOT NULL UNIQUE,PRIMARY KEY(run_id,task_id),FOREIGN KEY(run_id) REFERENCES check_runs(id) ON DELETE RESTRICT,FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT)`,
	`CREATE INDEX idx_tasks_node_phase ON tasks(node_id,phase,created_at)`,
	`CREATE INDEX idx_task_events_task_id ON task_events(task_id,id)`,
	`CREATE INDEX idx_check_run_tasks_run_id ON check_run_tasks(run_id,task_id)`,
	`CREATE TABLE operation_runs (
		id TEXT PRIMARY KEY,idempotency_key TEXT NOT NULL UNIQUE,operation_id TEXT NOT NULL,operation_version TEXT NOT NULL,
		capability_digest TEXT NOT NULL,node_id TEXT NOT NULL,targets_json TEXT NOT NULL,parameters_json TEXT NOT NULL,
		secret_refs_json TEXT NOT NULL,state TEXT NOT NULL,checkpoint TEXT NOT NULL,task_id TEXT NOT NULL UNIQUE,
		plan_digest TEXT NOT NULL DEFAULT '',discovery_json TEXT,precheck_json TEXT,plan_json TEXT,impact_json TEXT,
		block_json TEXT,recovery_json TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
		FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE RESTRICT,FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX idx_operation_runs_created ON operation_runs(created_at DESC,id DESC)`,
	`CREATE INDEX idx_operation_runs_operation ON operation_runs(operation_id,created_at DESC)`,
	`CREATE INDEX idx_operation_runs_node ON operation_runs(node_id,created_at DESC)`,
}
