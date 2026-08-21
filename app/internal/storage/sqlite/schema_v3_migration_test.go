package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/domain"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV2ToV3WithoutLosingNodes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{schemaStatements, schemaV2Statements} {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed v2 schema: %v", err)
			}
		}
	}
	now := time.Date(2026, 8, 4, 11, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '2', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO nodes(id, hostname, os, os_version, arch, agent_version, registered_at, last_seen_at, updated_at)
		VALUES('v2-node', 'node', 'linux', 'test', 'amd64', 'test', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := open(ctx, path, func() time.Time {
		return time.Date(2026, 8, 4, 11, 0, 30, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("migrate v2 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node, err := store.GetNode(ctx, "v2-node", time.Minute)
	if err != nil || node.ID != "v2-node" || node.ObservedSourceAddress != "" || node.Status != domain.NodeStatusOnline {
		t.Fatalf("migrated node=%#v err=%v", node, err)
	}
	for _, table := range []string{"sites", "check_runs", "check_run_tasks"} {
		var found string
		if err := store.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil || found != table {
			t.Fatalf("v3 table %s missing: found=%q err=%v", table, found, err)
		}
	}
}
