package sqlite

var schemaV2Statements = []string{
	`CREATE TABLE enrollment_tokens (
		id TEXT PRIMARY KEY,
		secret_digest BLOB NOT NULL,
		expires_at TEXT NOT NULL,
		max_uses INTEGER NOT NULL CHECK(max_uses > 0),
		uses INTEGER NOT NULL DEFAULT 0 CHECK(uses >= 0 AND uses <= max_uses),
		created_at TEXT NOT NULL,
		last_used_at TEXT,
		revoked_at TEXT
	)`,
	`CREATE TABLE agent_credentials (
		id TEXT PRIMARY KEY,
		agent_id TEXT NOT NULL,
		secret_digest BLOB NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT,
		last_used_at TEXT,
		revoked_at TEXT,
		rotated_from TEXT,
		FOREIGN KEY(rotated_from) REFERENCES agent_credentials(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE tasks (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		node_id TEXT NOT NULL,
		plugin_id TEXT NOT NULL,
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
		FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE RESTRICT,
		FOREIGN KEY(plugin_id) REFERENCES plugins(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE task_results (
		task_id TEXT PRIMARY KEY,
		result_json TEXT NOT NULL,
		result_digest BLOB NOT NULL,
		reported_at TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE task_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		phase TEXT NOT NULL,
		event_type TEXT NOT NULL,
		created_at TEXT NOT NULL,
		details_json TEXT NOT NULL,
		FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX idx_agent_credentials_agent_id ON agent_credentials(agent_id)`,
	`CREATE INDEX idx_tasks_node_phase ON tasks(node_id, phase, created_at)`,
	`CREATE INDEX idx_task_events_task_id ON task_events(task_id, id)`,
}
