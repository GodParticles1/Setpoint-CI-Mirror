package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestForeignKeysAreEnabled(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var enabled int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("read foreign key setting: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
}

func TestOpenRejectsUnknownSchemaVersionWithoutOverwritingIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE settings SET value = '99' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("set future schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("unknown schema version was accepted")
	}
}
