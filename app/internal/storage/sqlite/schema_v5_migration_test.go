package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/plugin"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesSchemaV4CheckRunsToGranularV5Selections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "setpoint.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statements := range [][]string{schemaStatements, schemaV2Statements, schemaV3Statements, schemaV4Statements} {
		for _, statement := range statements {
			if _, err := database.ExecContext(ctx, statement); err != nil {
				t.Fatalf("seed v4 schema: %v", err)
			}
		}
	}
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC).Format(timeFormat)
	if _, err := database.ExecContext(ctx,
		`INSERT INTO settings(key, value, updated_at) VALUES('schema_version', '4', ?)`, now); err != nil {
		t.Fatal(err)
	}
	definitions := []plugin.CheckItemDefinition{
		{ID: "ssh.permit_root_login", Name: "Root login", RecommendedValue: "no"},
		{ID: "ssh.max_auth_tries", Name: "Authentication attempts", RecommendedValue: "<=4"},
	}
	checks, _ := json.Marshal(definitions)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO plugins(id, category, name, version, description, mode, risk, impact, supported_systems, parameters, checks_json, updated_at)
		VALUES('linux.ssh.baseline', 'ssh', 'SSH', '1.0.0', 'ssh', 'read_only', 'low', 'none', '["linux"]', '[]', ?, ?)`,
		string(checks), now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO check_runs(id, idempotency_key, name, node_ids_json, plugin_ids_json, parameters_json, created_at)
		VALUES('v4-run', 'v4-idem', 'legacy', '[]', '["linux.ssh.baseline"]', '{}', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v4 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	run, err := store.GetCheckRun(ctx, "v4-run")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(run.Spec.CheckIDs, []string{"ssh.max_auth_tries", "ssh.permit_root_login"}) {
		t.Fatalf("migrated check IDs=%v", run.Spec.CheckIDs)
	}
	if !reflect.DeepEqual(run.Spec.BundleIDs, []string{"linux.ssh.baseline"}) || len(run.Spec.PolicyIDs) != 0 {
		t.Fatalf("migrated selection=%#v", run.Spec)
	}
}
