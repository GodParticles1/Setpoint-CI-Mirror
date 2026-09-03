package sqlite

// v17 persists only batch confirmation authorization/fan-out facts; child operation lifecycle remains in operation_runs.
// It is reconstruction metadata, not a batch execution state machine.
var schemaV17Statements = []string{
	`CREATE TABLE operation_batch_confirmations (
		batch_id TEXT PRIMARY KEY,
		source_check_run_id TEXT NOT NULL,
		confirmation_fingerprint TEXT NOT NULL,
		confirmation_idempotency_key TEXT NOT NULL UNIQUE,
		accepted_at TEXT NOT NULL,
		FOREIGN KEY(source_check_run_id) REFERENCES check_runs(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE operation_batch_confirmation_members (
		batch_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		task_id TEXT NOT NULL,
		check_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		run_id TEXT NOT NULL UNIQUE,
		plan_digest TEXT NOT NULL,
		fanout_state TEXT NOT NULL CHECK(fanout_state IN ('pending', 'confirmed', 'suppressed_canceled')),
		updated_at TEXT NOT NULL,
		PRIMARY KEY(batch_id, ordinal),
		UNIQUE(batch_id, task_id, check_id, node_id),
		FOREIGN KEY(batch_id) REFERENCES operation_batch_confirmations(batch_id) ON DELETE CASCADE,
		FOREIGN KEY(run_id) REFERENCES operation_runs(id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX idx_operation_batch_confirmations_source ON operation_batch_confirmations(source_check_run_id, accepted_at, batch_id)`,
	`CREATE INDEX idx_operation_batch_members_pending ON operation_batch_confirmation_members(fanout_state, batch_id, ordinal)`,
}
