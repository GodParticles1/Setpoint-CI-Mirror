package sqlite

var schemaV14Statements = []string{
	`CREATE TABLE clickhouse_restore_points (
		run_id TEXT NOT NULL,
		database_name TEXT NOT NULL,
		table_name TEXT NOT NULL,
		state TEXT NOT NULL CHECK(state IN ('intent','creating','ready','cleanup_pending','cleaned','manual_review')),
		record_json TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(run_id, database_name, table_name)
	)`,
	`CREATE INDEX idx_clickhouse_restore_points_run ON clickhouse_restore_points(run_id, database_name, table_name)`,
	`CREATE INDEX idx_clickhouse_restore_points_state ON clickhouse_restore_points(run_id, state, updated_at)`,
}
