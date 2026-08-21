package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/plugin"
	"setpoint/internal/task"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV3TasksToFrozenV4Contracts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{schemaStatements, schemaV2Statements, schemaV3Statements} {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed v3 schema: %v", err)
			}
		}
	}
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '3', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO nodes(id, hostname, os, os_version, arch, agent_version, registered_at, last_seen_at, updated_at)
		VALUES('v3-node', 'node', 'linux', 'test', 'amd64', 'test', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	definitions := []plugin.CheckItemDefinition{{
		ID: "audit.service.auditd", Name: "auditd", Description: "audit service", RecommendedValue: "active",
		SourceRefs: []string{"d9d10:module-46"},
	}}
	checks, _ := json.Marshal(definitions)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO plugins(id, category, name, version, description, mode, risk, impact, supported_systems, parameters, checks_json, updated_at)
		VALUES('linux.audit.core', 'audit', 'Audit', '1.0.0', 'audit', 'read_only', 'low', 'none', '["linux"]', '[]', ?, ?)`,
		string(checks), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO tasks(id, idempotency_key, node_id, plugin_id, parameters_json, phase, attempt, created_at, updated_at)
		VALUES('v3-task', 'v3-idem', 'v3-node', 'linux.audit.core', '{}', 'pending', 0, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v3 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	resource, err := store.GetTask(ctx, "v3-task")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Spec.Execution == nil || resource.Spec.Execution.PluginVersion != "1.0.0" ||
		len(resource.Spec.Execution.Checks) != 1 || resource.Spec.Execution.Checks[0].ID != "audit.service.auditd" {
		t.Fatalf("migrated contract=%#v", resource.Spec.Execution)
	}
	if err := task.ValidateCheckExecutionContract(*resource.Spec.Execution, resource.Spec.ContractDigest); err != nil {
		t.Fatalf("validate migrated contract: %v", err)
	}
}
