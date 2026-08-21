package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/task"
)

const checkRunColumns = `id, idempotency_key, name, node_ids_json, plugin_ids_json,
	definition_ids_json, bundle_ids_json, policy_ids_json, parameters_json, created_at`

func (store *Store) CreateCheckRun(
	ctx context.Context,
	run checkrun.Resource,
	tasks []task.Resource,
) (checkrun.Resource, bool, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return checkrun.Resource{}, false, fmt.Errorf("begin check run creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	existing, err := scanCheckRun(transaction.QueryRowContext(ctx,
		`SELECT `+checkRunColumns+` FROM check_runs WHERE idempotency_key = ?`, run.Metadata.IdempotencyKey))
	if err == nil {
		if !sameCheckRunSpec(existing, run) {
			return checkrun.Resource{}, false, domain.ErrIdempotencyConflict
		}
		if err := loadCheckRunTasks(ctx, transaction, &existing); err != nil {
			return checkrun.Resource{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return checkrun.Resource{}, false, err
	}

	encoded, err := encodeCheckRunSpec(run.Spec)
	if err != nil {
		return checkrun.Resource{}, false, err
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO check_runs(id, idempotency_key, name, node_ids_json, plugin_ids_json,
			definition_ids_json, bundle_ids_json, policy_ids_json, parameters_json, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.Metadata.ID, run.Metadata.IdempotencyKey, run.Metadata.Name,
		encoded.nodeIDs, encoded.legacyPluginIDs, encoded.checkIDs, encoded.bundleIDs, encoded.policyIDs,
		encoded.parameters, formatTime(run.Metadata.CreatedAt))
	if err != nil {
		return checkrun.Resource{}, false, fmt.Errorf("insert check run: %w", err)
	}
	for _, resource := range tasks {
		if err := insertCheckRunTask(ctx, transaction, run.Metadata.ID, resource); err != nil {
			return checkrun.Resource{}, false, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return checkrun.Resource{}, false, fmt.Errorf("commit check run creation: %w", err)
	}
	created, err := store.GetCheckRun(ctx, run.Metadata.ID)
	return created, true, err
}

func (store *Store) GetCheckRun(ctx context.Context, id string) (checkrun.Resource, error) {
	run, err := scanCheckRun(store.db.QueryRowContext(ctx,
		`SELECT `+checkRunColumns+` FROM check_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return checkrun.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return checkrun.Resource{}, err
	}
	if err := loadCheckRunTasks(ctx, store.db, &run); err != nil {
		return checkrun.Resource{}, err
	}
	return run, nil
}

func (store *Store) ListCheckRuns(ctx context.Context, limit, offset int) ([]checkrun.Resource, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+checkRunColumns+`
		FROM check_runs ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list check runs: %w", err)
	}
	runs := make([]checkrun.Resource, 0)
	for rows.Next() {
		run, err := scanCheckRun(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate check runs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close check run rows: %w", err)
	}
	for index := range runs {
		if err := loadCheckRunTasks(ctx, store.db, &runs[index]); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func insertCheckRunTask(ctx context.Context, transaction *sql.Tx, runID string, resource task.Resource) error {
	if err := insertTask(ctx, transaction, resource); err != nil {
		return fmt.Errorf("insert check run task: %w", err)
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO check_run_tasks(run_id, task_id) VALUES(?, ?)`, runID, resource.Metadata.ID); err != nil {
		return fmt.Errorf("associate check run task: %w", err)
	}
	return nil
}

type checkRunQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadCheckRunTasks(ctx context.Context, source checkRunQueryer, run *checkrun.Resource) error {
	rows, err := source.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks
		JOIN check_run_tasks ON check_run_tasks.task_id = tasks.id
		WHERE check_run_tasks.run_id = ? ORDER BY tasks.created_at, tasks.id`, run.Metadata.ID)
	if err != nil {
		return fmt.Errorf("list check run tasks: %w", err)
	}
	tasks := make([]task.Resource, 0)
	for rows.Next() {
		resource, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return err
		}
		tasks = append(tasks, resource)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate check run tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close check run task rows: %w", err)
	}
	for index := range tasks {
		if err := loadTaskResult(ctx, source, &tasks[index]); err != nil {
			return err
		}
	}
	run.Tasks = tasks
	run.Status = checkrun.Aggregate(tasks)
	return nil
}

func scanCheckRun(source scanner) (checkrun.Resource, error) {
	run := checkrun.Resource{APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun"}
	var nodeIDs, legacyPluginIDs, checkIDs, bundleIDs, policyIDs, parameters, createdAt string
	if err := source.Scan(&run.Metadata.ID, &run.Metadata.IdempotencyKey, &run.Metadata.Name,
		&nodeIDs, &legacyPluginIDs, &checkIDs, &bundleIDs, &policyIDs, &parameters, &createdAt); err != nil {
		return checkrun.Resource{}, err
	}
	for _, value := range []struct {
		name   string
		raw    string
		target *[]string
	}{
		{name: "node IDs", raw: nodeIDs, target: &run.Spec.NodeIDs},
		{name: "check IDs", raw: checkIDs, target: &run.Spec.CheckIDs},
		{name: "bundle IDs", raw: bundleIDs, target: &run.Spec.BundleIDs},
		{name: "policy IDs", raw: policyIDs, target: &run.Spec.PolicyIDs},
	} {
		if err := json.Unmarshal([]byte(value.raw), value.target); err != nil {
			return checkrun.Resource{}, fmt.Errorf("decode check run %s: %w", value.name, err)
		}
	}
	if err := json.Unmarshal([]byte(parameters), &run.Spec.Parameters); err != nil {
		return checkrun.Resource{}, fmt.Errorf("decode check run parameters: %w", err)
	}
	var err error
	run.Metadata.CreatedAt, err = parseTime(createdAt, "check run creation")
	if err != nil {
		return checkrun.Resource{}, err
	}
	return run, nil
}

type encodedCheckRunSpec struct {
	nodeIDs, legacyPluginIDs, checkIDs, bundleIDs, policyIDs, parameters string
}

func encodeCheckRunSpec(spec checkrun.Spec) (encodedCheckRunSpec, error) {
	values := []struct {
		name  string
		value any
		set   func(string)
	}{
		{name: "node IDs", value: spec.NodeIDs},
		{name: "legacy plugin IDs", value: selectedPluginIDs(spec)},
		{name: "check IDs", value: spec.CheckIDs},
		{name: "bundle IDs", value: spec.BundleIDs},
		{name: "policy IDs", value: spec.PolicyIDs},
		{name: "parameters", value: spec.Parameters},
	}
	encoded := encodedCheckRunSpec{}
	values[0].set = func(value string) { encoded.nodeIDs = value }
	values[1].set = func(value string) { encoded.legacyPluginIDs = value }
	values[2].set = func(value string) { encoded.checkIDs = value }
	values[3].set = func(value string) { encoded.bundleIDs = value }
	values[4].set = func(value string) { encoded.policyIDs = value }
	values[5].set = func(value string) { encoded.parameters = value }
	for _, value := range values {
		raw, err := json.Marshal(value.value)
		if err != nil {
			return encodedCheckRunSpec{}, fmt.Errorf("encode check run %s: %w", value.name, err)
		}
		value.set(string(raw))
	}
	return encoded, nil
}

func selectedPluginIDs(spec checkrun.Spec) []string {
	result := make([]string, 0, len(spec.Parameters))
	for pluginID := range spec.Parameters {
		result = append(result, pluginID)
	}
	sort.Strings(result)
	return result
}

func sameCheckRunSpec(left, right checkrun.Resource) bool {
	leftEncoded, leftErr := encodeCheckRunSpec(left.Spec)
	rightEncoded, rightErr := encodeCheckRunSpec(right.Spec)
	return leftErr == nil && rightErr == nil && left.Metadata.Name == right.Metadata.Name &&
		leftEncoded == rightEncoded
}
