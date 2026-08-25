package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

var schemaV15Statements = []string{
	`PRAGMA defer_foreign_keys = ON`,
	`CREATE TABLE tasks_v15 (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		node_id TEXT NOT NULL,
		plugin_id TEXT,
		parameters_json TEXT NOT NULL,
		execution_contract_json TEXT NOT NULL DEFAULT '',
		execution_contract_digest TEXT NOT NULL DEFAULT '',
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
		kind TEXT NOT NULL DEFAULT 'ReadOnlyCheckTask' CHECK(kind IN ('ReadOnlyCheckTask','OperationPlanningTask','OperationExecutionTask')),
		operation_id TEXT NOT NULL DEFAULT '',
		operation_version TEXT NOT NULL DEFAULT '',
		capability_digest TEXT NOT NULL DEFAULT '',
		targets_json TEXT NOT NULL DEFAULT '[]',
		secret_refs_json TEXT NOT NULL DEFAULT '[]',
		CHECK((kind = 'ReadOnlyCheckTask' AND plugin_id IS NOT NULL AND plugin_id <> '' AND operation_id = '') OR
			(kind = 'OperationPlanningTask' AND plugin_id IS NULL AND operation_id <> '' AND operation_version <> '' AND capability_digest <> '') OR
			(kind = 'OperationExecutionTask' AND plugin_id IS NULL AND operation_id <> '' AND operation_version <> '' AND capability_digest <> '' AND execution_contract_json <> '' AND execution_contract_digest <> '')),
		FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE RESTRICT,
		FOREIGN KEY(plugin_id) REFERENCES plugins(id) ON DELETE RESTRICT
	)`,
	`INSERT INTO tasks_v15(id,idempotency_key,node_id,plugin_id,parameters_json,execution_contract_json,execution_contract_digest,phase,claim_id,attempt,created_at,updated_at,claimed_at,acknowledged_at,cancel_requested_at,completed_at,last_error_code,last_error_message,kind,operation_id,operation_version,capability_digest,targets_json,secret_refs_json)
	 SELECT id,idempotency_key,node_id,plugin_id,parameters_json,execution_contract_json,execution_contract_digest,phase,claim_id,attempt,created_at,updated_at,claimed_at,acknowledged_at,cancel_requested_at,completed_at,last_error_code,last_error_message,kind,operation_id,operation_version,capability_digest,targets_json,secret_refs_json FROM tasks`,
	`CREATE TABLE task_results_v15 (task_id TEXT PRIMARY KEY,result_json TEXT NOT NULL,result_digest BLOB NOT NULL,reported_at TEXT NOT NULL,FOREIGN KEY(task_id) REFERENCES tasks_v15(id) ON DELETE RESTRICT)`,
	`INSERT INTO task_results_v15 SELECT task_id,result_json,result_digest,reported_at FROM task_results`,
	`CREATE TABLE task_events_v15 (id INTEGER PRIMARY KEY AUTOINCREMENT,task_id TEXT NOT NULL,phase TEXT NOT NULL,event_type TEXT NOT NULL,created_at TEXT NOT NULL,details_json TEXT NOT NULL,FOREIGN KEY(task_id) REFERENCES tasks_v15(id) ON DELETE RESTRICT)`,
	`INSERT INTO task_events_v15 SELECT id,task_id,phase,event_type,created_at,details_json FROM task_events`,
	`CREATE TABLE check_run_tasks_v15 (run_id TEXT NOT NULL,task_id TEXT NOT NULL UNIQUE,PRIMARY KEY(run_id,task_id),FOREIGN KEY(run_id) REFERENCES check_runs(id) ON DELETE RESTRICT,FOREIGN KEY(task_id) REFERENCES tasks_v15(id) ON DELETE RESTRICT)`,
	`INSERT INTO check_run_tasks_v15 SELECT run_id,task_id FROM check_run_tasks`,
	`CREATE TABLE operation_runs_v15 (
		id TEXT PRIMARY KEY,idempotency_key TEXT NOT NULL UNIQUE,operation_id TEXT NOT NULL,operation_version TEXT NOT NULL,
		capability_digest TEXT NOT NULL,node_id TEXT NOT NULL,targets_json TEXT NOT NULL,parameters_json TEXT NOT NULL,
		secret_refs_json TEXT NOT NULL,state TEXT NOT NULL,checkpoint TEXT NOT NULL,task_id TEXT NOT NULL UNIQUE,
		plan_digest TEXT NOT NULL DEFAULT '',discovery_json TEXT,precheck_json TEXT,plan_json TEXT,impact_json TEXT,
		block_json TEXT,recovery_json TEXT,execution_json TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,
		FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE RESTRICT,FOREIGN KEY(task_id) REFERENCES tasks_v15(id) ON DELETE RESTRICT
	)`,
	`INSERT INTO operation_runs_v15(id,idempotency_key,operation_id,operation_version,capability_digest,node_id,targets_json,parameters_json,secret_refs_json,state,checkpoint,task_id,plan_digest,discovery_json,precheck_json,plan_json,impact_json,block_json,recovery_json,execution_json,created_at,updated_at)
	 SELECT id,idempotency_key,operation_id,operation_version,capability_digest,node_id,targets_json,parameters_json,secret_refs_json,state,checkpoint,task_id,plan_digest,discovery_json,precheck_json,plan_json,impact_json,block_json,recovery_json,execution_json,created_at,updated_at FROM operation_runs`,
	`CREATE TABLE operation_journal_v15 (run_id TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence > 0),state TEXT NOT NULL,checkpoint TEXT NOT NULL DEFAULT '',message TEXT NOT NULL,at TEXT NOT NULL,evidence_json TEXT NOT NULL DEFAULT '[]',PRIMARY KEY(run_id,sequence),FOREIGN KEY(run_id) REFERENCES operation_runs_v15(id) ON DELETE RESTRICT)`,
	`INSERT INTO operation_journal_v15 SELECT run_id,sequence,state,checkpoint,message,at,evidence_json FROM operation_journal`,
	`DROP TABLE operation_journal`,
	`DROP TABLE operation_runs`,
	`DROP TABLE check_run_tasks`,
	`DROP TABLE task_results`,
	`DROP TABLE task_events`,
	`DROP TABLE tasks`,
	`ALTER TABLE tasks_v15 RENAME TO tasks`,
	`ALTER TABLE task_results_v15 RENAME TO task_results`,
	`ALTER TABLE task_events_v15 RENAME TO task_events`,
	`ALTER TABLE check_run_tasks_v15 RENAME TO check_run_tasks`,
	`ALTER TABLE operation_runs_v15 RENAME TO operation_runs`,
	`ALTER TABLE operation_journal_v15 RENAME TO operation_journal`,
	`CREATE INDEX idx_tasks_node_phase ON tasks(node_id,phase,created_at)`,
	`CREATE INDEX idx_task_events_task_id ON task_events(task_id,id)`,
	`CREATE INDEX idx_check_run_tasks_run_id ON check_run_tasks(run_id,task_id)`,
	`CREATE INDEX idx_operation_runs_created ON operation_runs(created_at DESC,id DESC)`,
	`CREATE INDEX idx_operation_runs_operation ON operation_runs(operation_id,created_at DESC)`,
	`CREATE INDEX idx_operation_runs_node ON operation_runs(node_id,created_at DESC)`,
	`CREATE INDEX idx_operation_journal_run ON operation_journal(run_id,sequence)`,
}

func migrateSchemaV15(ctx context.Context, transaction *sql.Tx) error {
	for _, statement := range schemaV15Statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema v15 statement: %w", err)
		}
	}
	return nil
}
