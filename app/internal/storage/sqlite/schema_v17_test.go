package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSchemaV16ToV17AddsDurableBatchConfirmationReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	seedHistoricalSchema(t, ctx, path, 16)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v16 to v17: %v", err)
	}
	defer store.Close()
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='schema_version'`).Scan(&version); err != nil || version != "17" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	assertTableExists(t, ctx, store.db, "operation_batch_confirmations")
	assertTableExists(t, ctx, store.db, "operation_batch_confirmation_members")
	assertForeignKeysClean(t, ctx, store.db)
}

func assertTableExists(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	var found string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&found); err != nil || found != name {
		t.Fatalf("table %s missing: found=%q err=%v", name, found, err)
	}
}
