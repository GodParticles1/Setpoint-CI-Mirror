package sqlite

var schemaV12Statements = []string{
	`CREATE TABLE operation_journal (
		run_id TEXT NOT NULL,
		sequence INTEGER NOT NULL CHECK(sequence > 0),
		state TEXT NOT NULL,
		checkpoint TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL,
		at TEXT NOT NULL,
		evidence_json TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY(run_id, sequence),
		FOREIGN KEY(run_id) REFERENCES operation_runs(id) ON DELETE RESTRICT
	)`,
	`CREATE INDEX idx_operation_journal_run ON operation_journal(run_id, sequence)`,
}
