package sqlite

var schemaV6Statements = []string{
	`CREATE TABLE operation_runs (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		operation_id TEXT NOT NULL,
		operation_version TEXT NOT NULL,
		node_id TEXT NOT NULL,
		parameters_json TEXT NOT NULL,
		secret_refs_json TEXT NOT NULL,
		plan_json TEXT NOT NULL DEFAULT '',
		plan_digest TEXT NOT NULL DEFAULT '',
		phase TEXT NOT NULL,
		restore_point_json TEXT NOT NULL DEFAULT '',
		failure_json TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		confirmed_at TEXT,
		completed_at TEXT
	)`,
	`CREATE TABLE operation_checkpoints (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id TEXT NOT NULL,
		name TEXT NOT NULL,
		phase TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY(run_id) REFERENCES operation_runs(id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX idx_operation_runs_created_at ON operation_runs(created_at DESC, id)`,
	`CREATE INDEX idx_operation_runs_phase ON operation_runs(phase, updated_at)`,
	`CREATE INDEX idx_operation_checkpoints_run_id ON operation_checkpoints(run_id, id)`,
}
