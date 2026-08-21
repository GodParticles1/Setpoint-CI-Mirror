package sqlite

var schemaV11Statements = []string{
	`ALTER TABLE operation_runs ADD COLUMN execution_json TEXT`,
}
