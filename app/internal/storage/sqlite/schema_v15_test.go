package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/task"

	_ "modernc.org/sqlite"
)

func TestFreshSchemaV15SupportsOperationExecutionTasksFailClosed(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	seedTaskDependencies(t, store, "node-v15", "check-v15", now)
	assertV15ExecutionTaskContract(t, ctx, store, "node-v15", now)
	assertForeignKeysClean(t, ctx, store.db)
}

func TestSchemaV15AndV16MigrationPreservesTaskAndOperationRunGraph(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	seedHistoricalSchema(t, ctx, path, 14)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Format(timeFormat)
	statements := []string{
		`INSERT INTO nodes(id,hostname,os,os_version,arch,agent_version,registered_at,last_seen_at,updated_at) VALUES('node-v14','node','linux','test','amd64','test','` + now + `','` + now + `','` + now + `')`,
		`INSERT INTO plugins(id,name,version,description,mode,risk,impact,supported_systems,parameters,updated_at) VALUES('check-v14','check','1','test','read_only','low','none','["linux"]','[]','` + now + `')`,
		`INSERT INTO check_runs(id,idempotency_key,name,node_ids_json,plugin_ids_json,parameters_json,created_at) VALUES('run-check-v14','run-check-idem','check','["node-v14"]','["check-v14"]','{}','` + now + `')`,
		`INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,created_at,updated_at,kind) VALUES('read-v14','read-idem','node-v14','check-v14','{}','succeeded','` + now + `','` + now + `','ReadOnlyCheckTask')`,
		`INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,created_at,updated_at,kind,operation_id,operation_version,capability_digest,targets_json) VALUES('plan-v14','plan-idem','node-v14',NULL,'{}','succeeded','` + now + `','` + now + `','OperationPlanningTask','test.operation','1.0.0','sha256:cap','[{"kind":"node","node_id":"node-v14"}]')`,
		`INSERT INTO task_results(task_id,result_json,result_digest,reported_at) VALUES('read-v14','{}',X'01','` + now + `')`,
		`INSERT INTO task_events(task_id,phase,event_type,created_at,details_json) VALUES('read-v14','succeeded','completed','` + now + `','{}')`,
		`INSERT INTO check_run_tasks(run_id,task_id) VALUES('run-check-v14','read-v14')`,
		`INSERT INTO operation_runs(id,idempotency_key,operation_id,operation_version,capability_digest,node_id,targets_json,parameters_json,secret_refs_json,state,checkpoint,task_id,plan_digest,created_at,updated_at) VALUES('run-v14','run-idem','test.operation','1.0.0','sha256:cap','node-v14','[{"kind":"node","node_id":"node-v14"}]','{}','[]','planned','planned','plan-v14','sha256:plan','` + now + `','` + now + `')`,
		`INSERT INTO operation_journal(run_id,sequence,state,checkpoint,message,at,evidence_json) VALUES('run-v14',1,'planned','planned','preserve','` + now + `','[]')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed v14 durable graph: %v", err)
		}
	}
	assertForeignKeysClean(t, ctx, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v14 to latest: %v", err)
	}
	defer store.Close()
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='schema_version'`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version=%q err=%v", version, err)
	}
	for id, kind := range map[string]string{"read-v14": task.KindReadOnlyCheckTask, "plan-v14": task.KindOperationPlanningTask} {
		var got string
		if err := store.db.QueryRowContext(ctx, `SELECT kind FROM tasks WHERE id=?`, id).Scan(&got); err != nil || got != kind {
			t.Fatalf("task %s kind=%q err=%v", id, got, err)
		}
	}
	for table, where := range map[string]string{"task_results": "task_id='read-v14'", "task_events": "task_id='read-v14'", "check_run_tasks": "task_id='read-v14'", "operation_journal": "run_id='run-v14'"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+where).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	var runTask string
	if err := store.db.QueryRowContext(ctx, `SELECT task_id FROM operation_runs WHERE id='run-v14'`).Scan(&runTask); err != nil || runTask != "plan-v14" {
		t.Fatalf("operation run task=%q err=%v", runTask, err)
	}
	assertOperationRunTaskFK(t, ctx, store.db)
	assertForeignKeysClean(t, ctx, store.db)
	assertV15ExecutionTaskContract(t, ctx, store, "node-v14", time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC))
	assertForeignKeysClean(t, ctx, store.db)
}

func assertV15ExecutionTaskContract(t *testing.T, ctx context.Context, store *Store, nodeID string, now time.Time) {
	t.Helper()
	target := operation.Target{Kind: operation.TargetNode, NodeID: nodeID}
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: "test.operation", RunID: "exec-run-" + nodeID, Action: task.OperationActionCreateRestorePoint,
		PlanDigest: "sha256:plan", Targets: []operation.Target{target},
		Plan: operation.Plan{SchemaVersion: "setpoint.operation.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{"action":"test"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource := task.Resource{APIVersion: "setpoint.io/v1", Kind: task.KindOperationExecutionTask,
		Metadata: task.Metadata{ID: "exec-task-" + nodeID, IdempotencyKey: "exec-idem-" + nodeID, CreatedAt: now},
		Spec:     task.Spec{NodeID: nodeID, OperationID: "test.operation", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap", Targets: []operation.Target{target}, Parameters: json.RawMessage(`{}`), OperationExecution: &contract, ContractDigest: digest},
	}
	created, wasCreated, err := store.CreateTask(ctx, resource)
	if err != nil || !wasCreated || created.Kind != task.KindOperationExecutionTask {
		t.Fatalf("create execution task: created=%v task=%#v err=%v", wasCreated, created, err)
	}
	bad := resource
	bad.Metadata.ID += "-bad"
	bad.Metadata.IdempotencyKey += "-bad"
	bad.Spec.ContractDigest = "tampered"
	if _, _, err := store.CreateTask(ctx, bad); err == nil {
		t.Fatal("mismatched execution contract digest accepted")
	}
	ts := formatTime(now.Add(time.Second))
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,created_at,updated_at,kind,operation_id,operation_version,capability_digest) VALUES(?,?,?,NULL,'{}','pending',?,?,'OperationExecutionTask','test.operation','1.0.0','sha256:cap')`, "missing-contract-"+nodeID, "missing-contract-idem-"+nodeID, nodeID, ts, ts); err == nil {
		t.Fatal("execution task without contract accepted")
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tasks(id,idempotency_key,node_id,plugin_id,parameters_json,phase,created_at,updated_at,kind) VALUES(?,?,?,NULL,'{}','pending',?,?,'UnexpectedTask')`, "bad-kind-"+nodeID, "bad-kind-idem-"+nodeID, nodeID, ts, ts); err == nil {
		t.Fatal("invalid task kind accepted")
	}
}

func assertOperationRunTaskFK(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(operation_runs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "tasks" && from == "task_id" && to == "id" {
			found = true
		}
	}
	if !found {
		t.Fatal("operation_runs.task_id no longer references tasks.id")
	}
}

func assertForeignKeysClean(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation")
	}
}
