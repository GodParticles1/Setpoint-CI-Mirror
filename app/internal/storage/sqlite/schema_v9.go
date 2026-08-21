package sqlite

var schemaV9Statements = []string{
	`CREATE TABLE clickhouse_migration_ledger (
		run_id TEXT NOT NULL,
		database_name TEXT NOT NULL,
		table_name TEXT NOT NULL,
		partition_key TEXT NOT NULL DEFAULT '',
		chunk INTEGER NOT NULL,
		strategy TEXT NOT NULL,
		state TEXT NOT NULL,
		attempt INTEGER NOT NULL,
		checkpoint TEXT NOT NULL DEFAULT '',
		staging_table TEXT NOT NULL DEFAULT '',
		source_rows INTEGER NOT NULL DEFAULT 0,
		source_bytes INTEGER NOT NULL DEFAULT 0,
		source_hash_sum64 TEXT NOT NULL DEFAULT '',
		source_hash_xor64 TEXT NOT NULL DEFAULT '',
		target_rows INTEGER NOT NULL DEFAULT 0,
		target_bytes INTEGER NOT NULL DEFAULT 0,
		target_hash_sum64 TEXT NOT NULL DEFAULT '',
		target_hash_xor64 TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(run_id, database_name, table_name, partition_key, chunk)
	)`,
	`CREATE INDEX idx_clickhouse_migration_ledger_run ON clickhouse_migration_ledger(run_id, database_name, table_name, partition_key, chunk)`,
	`CREATE INDEX idx_clickhouse_migration_ledger_state ON clickhouse_migration_ledger(run_id, state, updated_at)`,
}
