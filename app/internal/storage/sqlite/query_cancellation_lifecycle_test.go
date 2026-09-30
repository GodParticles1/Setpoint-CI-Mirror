package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlitedriver "modernc.org/sqlite"
)

func TestCanceledQueryReleasesReadLockAndDatabaseFile(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "canceled-query.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	// Each fixture registers its own barrier function for go test -count=N.
	function := fmt.Sprintf("sp106_cancel_%x", sha256.Sum256([]byte(databasePath)))
	err := sqlitedriver.RegisterScalarFunction(function, 0, func(*sqlitedriver.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-ctx.Done()
		return int64(42), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.Exec("CREATE TABLE evidence(value TEXT); INSERT INTO evidence VALUES('synthetic')"); err != nil {
		t.Fatal(err)
	}
	query := "SELECT " + function + "()," + strings.Repeat("value,", 499) + "value FROM evidence"
	completed := make(chan error, 1)
	go func() {
		rows, err := db.QueryContext(ctx, query)
		if rows != nil {
			_ = rows.Close()
		}
		completed <- err
	}()
	select {
	case <-entered: // Cancellation happens during query execution, never before it.
	case err := <-completed:
		t.Fatalf("query ended before entering cancellation barrier: %v", err)
	}
	cancel()
	if err := <-completed; !errors.Is(err, context.Canceled) {
		t.Fatalf("query result=%v; want cancellation", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("pool after close: %+v", db.Stats())
	reopened, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := reopened.Exec("UPDATE evidence SET value='updated'")
	closeErr := reopened.Close()
	if writeErr != nil {
		t.Errorf("read lock survived database.Close: %v", writeErr)
	}
	if closeErr != nil {
		t.Error(closeErr)
	}
	if err := os.Remove(databasePath); err != nil {
		t.Errorf("file handle survived all connection closes: %v", err)
	}
}
