package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"setpoint/internal/plugin"
)

var schemaV5Statements = []string{
	`ALTER TABLE check_runs ADD COLUMN definition_ids_json TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE check_runs ADD COLUMN bundle_ids_json TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE check_runs ADD COLUMN policy_ids_json TEXT NOT NULL DEFAULT '[]'`,
}

func migrateSchemaV5(ctx context.Context, transaction *sql.Tx) error {
	for _, statement := range schemaV5Statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema v5 statement: %w", err)
		}
	}
	type legacyRun struct {
		id, pluginIDs string
	}
	rows, err := transaction.QueryContext(ctx, `SELECT id, plugin_ids_json FROM check_runs`)
	if err != nil {
		return fmt.Errorf("list legacy check runs for schema v5: %w", err)
	}
	legacy := make([]legacyRun, 0)
	for rows.Next() {
		var current legacyRun
		if err := rows.Scan(&current.id, &current.pluginIDs); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy check run for schema v5: %w", err)
		}
		legacy = append(legacy, current)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate legacy check runs for schema v5: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy check run rows for schema v5: %w", err)
	}
	for _, current := range legacy {
		pluginIDs, err := decodeLegacyPluginIDs(current)
		if err != nil {
			return err
		}
		definitionIDs := make([]string, 0)
		seen := make(map[string]struct{})
		for _, pluginID := range pluginIDs {
			var encoded string
			if err := transaction.QueryRowContext(ctx,
				`SELECT checks_json FROM plugins WHERE id = ?`, pluginID).Scan(&encoded); err != nil {
				return fmt.Errorf("load persisted checks for legacy run %s plugin %s: %w", current.id, pluginID, err)
			}
			var definitions []plugin.CheckItemDefinition
			if err := json.Unmarshal([]byte(encoded), &definitions); err != nil {
				return fmt.Errorf("decode persisted checks for legacy run %s plugin %s: %w", current.id, pluginID, err)
			}
			if len(definitions) == 0 {
				return fmt.Errorf("legacy check run %s plugin %s has no persisted check definitions", current.id, pluginID)
			}
			for _, definition := range definitions {
				id := strings.TrimSpace(definition.ID)
				if id == "" {
					return fmt.Errorf("legacy check run %s plugin %s has an empty persisted check ID", current.id, pluginID)
				}
				if _, exists := seen[id]; exists {
					continue
				}
				seen[id] = struct{}{}
				definitionIDs = append(definitionIDs, id)
			}
		}
		sort.Strings(definitionIDs)
		definitionsJSON, err := json.Marshal(definitionIDs)
		if err != nil {
			return fmt.Errorf("encode migrated definitions for legacy run %s: %w", current.id, err)
		}
		bundlesJSON, err := json.Marshal(pluginIDs)
		if err != nil {
			return fmt.Errorf("encode migrated bundles for legacy run %s: %w", current.id, err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE check_runs SET definition_ids_json = ?, bundle_ids_json = ? WHERE id = ?`,
			string(definitionsJSON), string(bundlesJSON), current.id); err != nil {
			return fmt.Errorf("persist migrated selection for legacy run %s: %w", current.id, err)
		}
	}
	return nil
}

func decodeLegacyPluginIDs(current struct{ id, pluginIDs string }) ([]string, error) {
	var pluginIDs []string
	if err := json.Unmarshal([]byte(current.pluginIDs), &pluginIDs); err != nil {
		return nil, fmt.Errorf("decode plugin IDs for legacy run %s: %w", current.id, err)
	}
	seen := make(map[string]struct{}, len(pluginIDs))
	result := make([]string, 0, len(pluginIDs))
	for _, value := range pluginIDs {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("legacy check run %s has an empty plugin ID", current.id)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}
