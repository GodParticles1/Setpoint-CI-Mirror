package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRC1SQLiteIntegrityAfterStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err := reopened.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		_ = reopened.Close()
		t.Fatalf("SQLite integrity_check: %v", err)
	}
	if result != "ok" {
		_ = reopened.Close()
		t.Fatalf("SQLite integrity_check=%q, want ok", result)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
