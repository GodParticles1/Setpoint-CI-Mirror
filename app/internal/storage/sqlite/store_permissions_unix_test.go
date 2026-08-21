//go:build !windows

package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesPrivateSQLiteFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(directory, "setpoint.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat SQLite file: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("SQLite permissions=%#o, want 0600", permissions)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat SQLite directory: %v", err)
	}
	if permissions := directoryInfo.Mode().Perm(); permissions&0o027 != 0 {
		t.Fatalf("SQLite directory permissions=%#o, group/other write or other access must be disabled", permissions)
	}
}

func TestOpenPreservesExistingSQLiteFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatalf("create existing file: %v", err)
	}
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat SQLite file: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o640 {
		t.Fatalf("existing SQLite permissions=%#o, want preserved 0640", permissions)
	}
}
