package sqlite

var schemaV8Statements = []string{
	`ALTER TABLE sites ADD COLUMN trusted_executable_roots_json TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE nodes ADD COLUMN trusted_executable_roots_json TEXT NOT NULL DEFAULT '[]'`,
}
