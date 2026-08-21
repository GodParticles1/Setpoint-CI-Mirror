package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateIDIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "agent-id")
	first, err := LoadOrCreateID(path)
	if err != nil {
		t.Fatalf("create ID: %v", err)
	}
	second, err := LoadOrCreateID(path)
	if err != nil {
		t.Fatalf("load ID: %v", err)
	}
	if first != second || !validUUID(first) {
		t.Fatalf("identity is not stable UUID: first=%q second=%q", first, second)
	}
}

func TestLoadOrCreateIDRejectsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-id")
	if err := os.WriteFile(path, []byte("not-an-id\n"), 0o600); err != nil {
		t.Fatalf("write invalid ID: %v", err)
	}
	if _, err := LoadOrCreateID(path); err == nil {
		t.Fatal("invalid identity file was accepted")
	}
}
