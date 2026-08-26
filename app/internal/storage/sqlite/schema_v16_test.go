package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestFreshSchemaV16HasNodeRetirementTombstone(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='schema_version'`).Scan(&version); err != nil || version != "16" {
		t.Fatalf("schema version=%q err=%v", version, err)
	}
	assertColumnExists(t, ctx, store.db, "nodes", "retired_at")
	assertForeignKeysClean(t, ctx, store.db)
}

func TestSchemaV15ToV16PreservesActiveNode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	seedHistoricalSchema(t, ctx, path, 15)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx, `INSERT INTO nodes
		(id,hostname,os,os_version,arch,agent_version,registered_at,last_seen_at,updated_at)
		VALUES('v15-active','node','linux','test','amd64','test',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v15 to v16: %v", err)
	}
	defer store.Close()
	if _, err := store.GetNode(ctx, "v15-active", time.Minute); err != nil {
		t.Fatalf("migrated active node hidden: %v", err)
	}
	var retired any
	if err := store.db.QueryRowContext(ctx, `SELECT retired_at FROM nodes WHERE id='v15-active'`).Scan(&retired); err != nil || retired != nil {
		t.Fatalf("migrated retired_at=%v err=%v", retired, err)
	}
	assertForeignKeysClean(t, ctx, store.db)
}

func assertColumnExists(t *testing.T, ctx context.Context, database *sql.DB, table, column string) {
	t.Helper()
	rows, err := database.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == column {
			found = true
		}
	}
	if !found {
		t.Fatalf("column %s.%s missing", table, column)
	}
}
