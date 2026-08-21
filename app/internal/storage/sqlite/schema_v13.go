package sqlite

var schemaV13Statements = []string{
	`CREATE TABLE operation_leases (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		resources_json TEXT NOT NULL,
		acquired_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`,
	`CREATE TABLE operation_lock_resources (
		resource_key TEXT PRIMARY KEY,
		lease_id TEXT NOT NULL,
		FOREIGN KEY(lease_id) REFERENCES operation_leases(id) ON DELETE CASCADE
	)`,
	`CREATE UNIQUE INDEX idx_operation_leases_owner ON operation_leases(owner_id)`,
	`CREATE INDEX idx_operation_lock_resources_lease ON operation_lock_resources(lease_id, resource_key)`,
	`CREATE INDEX idx_operation_leases_expires ON operation_leases(expires_at)`,
}
