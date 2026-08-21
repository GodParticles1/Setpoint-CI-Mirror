package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"setpoint/internal/plugin"
)

func (store *Store) UpsertCheck(ctx context.Context, metadata plugin.Metadata) error {
	systems, err := json.Marshal(metadata.SupportedSystems)
	if err != nil {
		return fmt.Errorf("encode supported systems: %w", err)
	}
	parameters, err := json.Marshal(metadata.Parameters)
	if err != nil {
		return fmt.Errorf("encode plugin parameters: %w", err)
	}
	checks, err := json.Marshal(metadata.Checks)
	if err != nil {
		return fmt.Errorf("encode plugin checks: %w", err)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO plugins(id, category, name, version, description, mode, risk, impact, supported_systems, parameters, checks_json, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			category = excluded.category,
			name = excluded.name,
			version = excluded.version,
			description = excluded.description,
			mode = excluded.mode,
			risk = excluded.risk,
			impact = excluded.impact,
			supported_systems = excluded.supported_systems,
			parameters = excluded.parameters,
			checks_json = excluded.checks_json,
			updated_at = excluded.updated_at`,
		metadata.ID, metadata.Category, metadata.Name, metadata.Version, metadata.Description, metadata.Mode,
		metadata.Risk, metadata.Impact, string(systems), string(parameters), string(checks), store.now().UTC().Format(timeFormat))
	if err != nil {
		return fmt.Errorf("upsert plugin metadata: %w", err)
	}
	return nil
}
