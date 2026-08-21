package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

var schemaV4Statements = []string{
	`ALTER TABLE tasks ADD COLUMN execution_contract_json TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE tasks ADD COLUMN execution_contract_digest TEXT NOT NULL DEFAULT ''`,
}

func migrateSchemaV4(ctx context.Context, transaction *sql.Tx) error {
	for _, statement := range schemaV4Statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema v4 statement: %w", err)
		}
	}
	type legacyTask struct {
		id, pluginID, pluginVersion, parameters, checks string
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT tasks.id, tasks.plugin_id, plugins.version, tasks.parameters_json, plugins.checks_json
		FROM tasks JOIN plugins ON plugins.id = tasks.plugin_id
		WHERE tasks.execution_contract_json = ''`)
	if err != nil {
		return fmt.Errorf("list legacy tasks for schema v4: %w", err)
	}
	legacy := make([]legacyTask, 0)
	for rows.Next() {
		var current legacyTask
		if err := rows.Scan(&current.id, &current.pluginID, &current.pluginVersion, &current.parameters, &current.checks); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan legacy task for schema v4: %w", err)
		}
		legacy = append(legacy, current)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate legacy tasks for schema v4: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close legacy task rows for schema v4: %w", err)
	}
	for _, current := range legacy {
		var definitions []plugin.CheckItemDefinition
		if err := json.Unmarshal([]byte(current.checks), &definitions); err != nil {
			return fmt.Errorf("decode checks for legacy task %s: %w", current.id, err)
		}
		if len(definitions) == 0 {
			return fmt.Errorf("legacy task %s has no persisted check definitions", current.id)
		}
		snapshots := make([]task.CheckDefinitionSnapshot, 0, len(definitions))
		for _, definition := range definitions {
			snapshots = append(snapshots, task.CheckDefinitionSnapshot{
				ID: definition.ID, Name: definition.Name, Description: definition.Description,
				RecommendedValue: definition.RecommendedValue, SourceRefs: append([]string(nil), definition.SourceRefs...),
			})
		}
		contract, digest, err := task.NewCheckExecutionContract(
			current.pluginID, current.pluginVersion, json.RawMessage(current.parameters), snapshots)
		if err != nil {
			return fmt.Errorf("freeze legacy task %s: %w", current.id, err)
		}
		encoded, err := json.Marshal(contract)
		if err != nil {
			return fmt.Errorf("encode legacy task %s contract: %w", current.id, err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE tasks SET execution_contract_json = ?, execution_contract_digest = ? WHERE id = ?`,
			string(encoded), digest, current.id); err != nil {
			return fmt.Errorf("persist legacy task %s contract: %w", current.id, err)
		}
	}
	return nil
}
