package sqlite

var schemaV7Statements = []string{
	`ALTER TABLE operation_runs ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
}
