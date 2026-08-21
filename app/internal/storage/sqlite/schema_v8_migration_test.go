package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV7TrustedExecutableRootColumnsToV8(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{
		schemaStatements, schemaV2Statements, schemaV3Statements, schemaV4Statements,
		schemaV5Statements, schemaV6Statements, schemaV7Statements,
	} {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed v7 schema: %v", err)
			}
		}
	}
	now := time.Now().UTC().Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '7', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO nodes(id, hostname, os, os_version, arch, agent_version, registered_at, last_seen_at, updated_at)
		VALUES('v7-node', 'node', 'linux', 'test', 'amd64', 'test', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v7 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node, err := store.GetNode(ctx, "v7-node", 0)
	if err != nil || len(node.TrustedExecutableRoots) != 0 {
		t.Fatalf("node=%#v err=%v", node, err)
	}
}
