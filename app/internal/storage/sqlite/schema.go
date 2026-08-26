package sqlite

import (
	"context"
	"fmt"
)

const schemaVersion = "16"

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		hostname TEXT NOT NULL,
		os TEXT NOT NULL,
		os_version TEXT NOT NULL,
		arch TEXT NOT NULL,
		agent_version TEXT NOT NULL,
		registered_at TEXT NOT NULL,
		last_seen_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS agent_registrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id TEXT NOT NULL,
		registered_at TEXT NOT NULL,
		FOREIGN KEY(agent_id) REFERENCES nodes(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE IF NOT EXISTS agent_heartbeats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id TEXT NOT NULL,
		received_at TEXT NOT NULL,
		FOREIGN KEY(agent_id) REFERENCES nodes(id) ON DELETE RESTRICT
	)`,
	`CREATE TABLE IF NOT EXISTS plugins (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		description TEXT NOT NULL,
		mode TEXT NOT NULL,
		risk TEXT NOT NULL,
		impact TEXT NOT NULL,
		supported_systems TEXT NOT NULL,
		parameters TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_registrations_agent_id ON agent_registrations(agent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_heartbeats_agent_id ON agent_heartbeats(agent_id)`,
}

func (store *Store) initialize(ctx context.Context) error {
	for _, statement := range []string{`PRAGMA foreign_keys = ON`, `PRAGMA busy_timeout = 5000`} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite connection: %w", err)
		}
	}

	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite initialization: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	for _, statement := range schemaStatements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize SQLite schema: %w", err)
		}
	}
	now := store.now().UTC().Format(timeFormat)
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '1', ?)
		ON CONFLICT(key) DO NOTHING`, now); err != nil {
		return fmt.Errorf("record SQLite schema version: %w", err)
	}
	var actual string
	if err := transaction.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'schema_version'`).Scan(&actual); err != nil {
		return fmt.Errorf("read SQLite schema version before migration: %w", err)
	}
	phase3Lineage, err := isPhase3SchemaLineage(ctx, transaction, actual)
	if err != nil {
		return fmt.Errorf("identify SQLite schema lineage: %w", err)
	}
	if phase3Lineage {
		if err := migratePhase3SchemaLineage(ctx, transaction, actual); err != nil {
			return fmt.Errorf("reconcile Phase 3 SQLite schema %s: %w", actual, err)
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '14', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record reconciled SQLite schema v14: %w", err)
		}
		actual = "14"
	}
	if actual == "1" {
		for _, statement := range schemaV2Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v2: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '2', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v2: %w", err)
		}
		actual = "2"
	}
	if actual == "2" {
		for _, statement := range schemaV3Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v3: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '3', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v3: %w", err)
		}
		actual = "3"
	}
	if actual == "3" {
		if err := migrateSchemaV4(ctx, transaction); err != nil {
			return fmt.Errorf("migrate SQLite schema to v4: %w", err)
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '4', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v4: %w", err)
		}
		actual = "4"
	}
	if actual == "4" {
		if err := migrateSchemaV5(ctx, transaction); err != nil {
			return fmt.Errorf("migrate SQLite schema to v5: %w", err)
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '5', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v5: %w", err)
		}
		actual = "5"
	}
	if actual == "5" {
		for _, statement := range schemaV6Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v6: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '6', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v6: %w", err)
		}
		actual = "6"
	}
	if actual == "6" {
		for _, statement := range schemaV7Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v7: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '7', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v7: %w", err)
		}
		actual = "7"
	}
	if actual == "7" {
		for _, statement := range schemaV8Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v8: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '8', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v8: %w", err)
		}
		actual = "8"
	}
	if actual == "8" {
		for _, statement := range schemaV9Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v9: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '9', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v9: %w", err)
		}
		actual = "9"
	}
	if actual == "9" {
		if err := migrateSchemaV10(ctx, transaction); err != nil {
			return fmt.Errorf("migrate SQLite schema to v10: %w", err)
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '10', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v10: %w", err)
		}
		actual = "10"
	}
	if actual == "10" {
		for _, statement := range schemaV11Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v11: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '11', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v11: %w", err)
		}
		actual = "11"
	}
	if actual == "11" {
		for _, statement := range schemaV12Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v12: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '12', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v12: %w", err)
		}
		actual = "12"
	}
	if actual == "12" {
		for _, statement := range schemaV13Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v13: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '13', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v13: %w", err)
		}
		actual = "13"
	}
	if actual == "13" {
		for _, statement := range schemaV14Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v14: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '14', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v14: %w", err)
		}
		actual = "14"
	}
	if actual == "14" {
		if err := migrateSchemaV15(ctx, transaction); err != nil {
			return fmt.Errorf("migrate SQLite schema to v15: %w", err)
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = '15', updated_at = ? WHERE key = 'schema_version'`, now); err != nil {
			return fmt.Errorf("record SQLite schema v15: %w", err)
		}
		actual = "15"
	}
	if actual == "15" {
		for _, statement := range schemaV16Statements {
			if _, err := transaction.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migrate SQLite schema to v16: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx,
			`UPDATE settings SET value = ?, updated_at = ? WHERE key = 'schema_version'`, schemaVersion, now); err != nil {
			return fmt.Errorf("record SQLite schema v16: %w", err)
		}
		actual = schemaVersion
	}
	if actual != schemaVersion {
		return fmt.Errorf("unsupported SQLite schema version %q; expected %q", actual, schemaVersion)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit SQLite initialization: %w", err)
	}
	if err := store.verifySchemaVersion(ctx); err != nil {
		return err
	}
	return nil
}

func (store *Store) verifySchemaVersion(ctx context.Context) error {
	var actual string
	if err := store.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'schema_version'`).Scan(&actual); err != nil {
		return fmt.Errorf("read SQLite schema version: %w", err)
	}
	if actual != schemaVersion {
		return fmt.Errorf("unsupported SQLite schema version %q; expected %q", actual, schemaVersion)
	}
	return nil
}
