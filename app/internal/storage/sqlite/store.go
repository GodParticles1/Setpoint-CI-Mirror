package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, time.Now)
}

func open(ctx context.Context, path string, now func() time.Time) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is required")
	}
	if !strings.HasPrefix(path, "file:") && path != ":memory:" {
		cleanPath := filepath.Clean(path)
		directory := filepath.Dir(cleanPath)
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return nil, fmt.Errorf("create SQLite directory: %w", err)
		}
		if err := createSQLiteFile(cleanPath); err != nil {
			return nil, err
		}
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	database.SetMaxOpenConns(1)
	store := &Store{db: database, now: now}
	if err := store.initialize(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	return store, nil
}

func createSQLiteFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create SQLite file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close new SQLite file: %w", err)
	}
	return nil
}

func (store *Store) Ping(ctx context.Context) error {
	if err := store.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping SQLite: %w", err)
	}
	return nil
}

func (store *Store) Close() error {
	if err := store.db.Close(); err != nil {
		return fmt.Errorf("close SQLite: %w", err)
	}
	return nil
}
