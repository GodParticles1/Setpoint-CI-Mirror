package sqlite

var schemaV3Statements = []string{
	`CREATE TABLE sites (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`ALTER TABLE nodes ADD COLUMN site_id TEXT REFERENCES sites(id) ON DELETE RESTRICT`,
	`ALTER TABLE nodes ADD COLUMN reported_address TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE nodes ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE nodes ADD COLUMN notes TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE plugins ADD COLUMN category TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE plugins ADD COLUMN checks_json TEXT NOT NULL DEFAULT '[]'`,
	`CREATE TABLE check_runs (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		node_ids_json TEXT NOT NULL,
		plugin_ids_json TEXT NOT NULL,
		parameters_json TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE check_run_tasks (
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL UNIQUE,
		PRIMARY KEY(run_id, task_id),
		FOREIGN KEY(run_id) REFERENCES check_runs(id) ON DELETE RESTRICT,
		FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX idx_nodes_site_id ON nodes(site_id)`,
	`CREATE INDEX idx_check_runs_created_at ON check_runs(created_at DESC, id)`,
	`CREATE INDEX idx_check_run_tasks_run_id ON check_run_tasks(run_id, task_id)`,
}
