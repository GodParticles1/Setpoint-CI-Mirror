package sqlite

var schemaV16Statements = []string{
	`ALTER TABLE nodes ADD COLUMN retired_at TEXT`,
	`CREATE INDEX idx_nodes_retired_at ON nodes(retired_at, id)`,
}
