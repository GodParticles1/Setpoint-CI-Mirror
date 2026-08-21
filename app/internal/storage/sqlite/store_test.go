package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/plugin"
)

func TestOpenInitializesSchemaIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, table := range []string{"nodes", "agent_registrations", "agent_heartbeats", "plugins", "settings"} {
		var found string
		err := store.db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found)
		if err != nil || found != table {
			t.Fatalf("table %s not initialized: found=%q err=%v", table, found, err)
		}
	}
	var version string
	if err := store.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("schema version = %q, want %q", version, schemaVersion)
	}
}

func TestRegistrationHeartbeatAndOnlineStatus(t *testing.T) {
	ctx := context.Background()
	current := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return current })
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	registration := domain.Registration{
		AgentID: "agent-1", Hostname: "node-1", OS: "linux", OSVersion: "debian 12",
		Arch: "amd64", AgentVersion: "0.1.0", ReceivedAt: current,
	}
	node, err := store.RegisterNode(ctx, registration)
	if err != nil {
		t.Fatalf("register node: %v", err)
	}
	if node.Status != domain.NodeStatusOnline || !node.RegisteredAt.Equal(current) {
		t.Fatalf("unexpected registered node: %#v", node)
	}

	current = current.Add(10 * time.Second)
	registration.Hostname = "node-renamed"
	registration.ReceivedAt = current
	node, err = store.RegisterNode(ctx, registration)
	if err != nil {
		t.Fatalf("register node again: %v", err)
	}
	if node.Hostname != "node-renamed" || !node.RegisteredAt.Equal(registration.ReceivedAt.Add(-10*time.Second)) {
		t.Fatalf("registration did not preserve creation time or update facts: %#v", node)
	}

	current = current.Add(5 * time.Second)
	if err := store.RecordHeartbeat(ctx, registration.AgentID, current); err != nil {
		t.Fatalf("record heartbeat: %v", err)
	}
	node, err = store.GetNode(ctx, registration.AgentID, 30*time.Second)
	if err != nil {
		t.Fatalf("get online node: %v", err)
	}
	if node.Status != domain.NodeStatusOnline || !node.LastSeenAt.Equal(current) {
		t.Fatalf("unexpected online node: %#v", node)
	}

	current = current.Add(31 * time.Second)
	node, err = store.GetNode(ctx, registration.AgentID, 30*time.Second)
	if err != nil {
		t.Fatalf("get offline node: %v", err)
	}
	if node.Status != domain.NodeStatusOffline {
		t.Fatalf("status = %q, want offline", node.Status)
	}

	var registrations, heartbeats int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_registrations`).Scan(&registrations); err != nil {
		t.Fatalf("count registrations: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_heartbeats`).Scan(&heartbeats); err != nil {
		t.Fatalf("count heartbeats: %v", err)
	}
	if registrations != 2 || heartbeats != 1 {
		t.Fatalf("history counts registrations=%d heartbeats=%d", registrations, heartbeats)
	}
}

func TestHeartbeatRejectsUnknownAgent(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	err = store.RecordHeartbeat(context.Background(), "missing", time.Now())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestUpsertCheckIsIdempotent(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	metadata := plugin.Metadata{
		ID: "dev.test", Name: "Test", Version: "1", Description: "test",
		Mode: plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "none",
		SupportedSystems: []string{"linux"}, Parameters: []plugin.Parameter{},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.UpsertCheck(context.Background(), metadata); err != nil {
			t.Fatalf("upsert plugin attempt %d: %v", attempt+1, err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = ?`, metadata.ID).Scan(&count); err != nil {
		t.Fatalf("count plugins: %v", err)
	}
	if count != 1 {
		t.Fatalf("plugin rows = %d, want 1", count)
	}
}
