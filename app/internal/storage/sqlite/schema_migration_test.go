package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV1ToV2WithoutLosingNodes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	for _, statement := range schemaStatements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create v1 schema: %v", err)
		}
	}
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '1', ?)`, now); err != nil {
		t.Fatalf("seed schema version: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO nodes(id, hostname, os, os_version, arch, agent_version, registered_at, last_seen_at, updated_at)
		VALUES('agent-before-migration', 'node', 'linux', 'test', 'amd64', 'test', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open and migrate store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetNode(ctx, "agent-before-migration", time.Minute); err != nil {
		t.Fatalf("read node after migration: %v", err)
	}
	for _, table := range []string{"enrollment_tokens", "agent_credentials", "tasks", "task_results", "task_events"} {
		var found string
		if err := store.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil || found != table {
			t.Fatalf("v2 table %s missing: found=%q err=%v", table, found, err)
		}
	}
}
