package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/task"
)

const taskColumns = `id, idempotency_key, node_id, plugin_id, kind, operation_id, operation_version,
	capability_digest, targets_json, secret_refs_json, parameters_json,
	execution_contract_json, execution_contract_digest, phase, claim_id, attempt,
	created_at, updated_at, claimed_at, acknowledged_at, cancel_requested_at, completed_at,
	last_error_code, last_error_message`

func (store *Store) CreateTask(ctx context.Context, resource task.Resource) (task.Resource, bool, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Resource{}, false, fmt.Errorf("begin task creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	existing, err := getTaskByIdempotencyKey(ctx, transaction, resource.Metadata.IdempotencyKey)
	if err == nil {
		if !sameTaskSpec(existing, resource) {
			return task.Resource{}, false, task.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return task.Resource{}, false, err
	}
	if err := insertTask(ctx, transaction, resource); err != nil {
		return task.Resource{}, false, err
	}
	if err := transaction.Commit(); err != nil {
		return task.Resource{}, false, fmt.Errorf("commit task creation: %w", err)
	}
	created, err := store.GetTask(ctx, resource.Metadata.ID)
	return created, true, err
}

func (store *Store) GetTask(ctx context.Context, id string) (task.Resource, error) {
	resource, err := scanTask(store.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return task.Resource{}, err
	}
	if err := loadTaskResult(ctx, store.db, &resource); err != nil {
		return task.Resource{}, err
	}
	return resource, nil
}

func (store *Store) ListTasks(ctx context.Context) ([]task.Resource, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	resources := make([]task.Resource, 0)
	for rows.Next() {
		resource, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close task rows: %w", err)
	}
	for index := range resources {
		if err := loadTaskResult(ctx, store.db, &resources[index]); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func (store *Store) ClaimTask(
	ctx context.Context,
	agentID string,
	claimID string,
	claimedAt time.Time,
) (*task.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin task claim: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	existing, err := scanTask(transaction.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE node_id = ? AND phase IN (?, ?, ?) ORDER BY created_at, id LIMIT 1`,
		agentID, task.PhaseClaimed, task.PhaseRunning, task.PhaseCancelRequested))
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	pending, err := scanTask(transaction.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE node_id = ? AND phase = ? ORDER BY created_at, id LIMIT 1`, agentID, task.PhasePending))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	timestamp := formatTime(claimedAt)
	result, err := transaction.ExecContext(ctx, `
		UPDATE tasks SET phase = ?, claim_id = ?, attempt = attempt + 1,
			claimed_at = ?, updated_at = ? WHERE id = ? AND phase = ?`,
		task.PhaseClaimed, claimID, timestamp, timestamp, pending.Metadata.ID, task.PhasePending)
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read task claim count: %w", err)
	}
	if updated != 1 {
		return nil, fmt.Errorf("%w: task was claimed concurrently", task.ErrInvalidTransition)
	}
	if err := insertTaskEvent(ctx, transaction, pending.Metadata.ID, task.PhaseClaimed, "claimed", claimedAt); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit task claim: %w", err)
	}
	claimed, err := store.GetTask(ctx, pending.Metadata.ID)
	if err != nil {
		return nil, err
	}
	return &claimed, nil
}

func (store *Store) AcknowledgeTask(
	ctx context.Context,
	agentID, taskID, claimID string,
	acknowledgedAt time.Time,
) (task.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Resource{}, fmt.Errorf("begin task acknowledgement: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	resource, err := getTaskForAgent(ctx, transaction, agentID, taskID, claimID)
	if err != nil {
		return task.Resource{}, err
	}
	if resource.Status.Phase == task.PhaseRunning || resource.Status.Phase == task.PhaseCancelRequested {
		return resource, nil
	}
	if resource.Status.Phase != task.PhaseClaimed {
		return task.Resource{}, fmt.Errorf("%w: cannot acknowledge task in phase %s", task.ErrInvalidTransition, resource.Status.Phase)
	}
	timestamp := formatTime(acknowledgedAt)
	if _, err := transaction.ExecContext(ctx, `
		UPDATE tasks SET phase = ?, acknowledged_at = ?, updated_at = ? WHERE id = ?`,
		task.PhaseRunning, timestamp, timestamp, taskID); err != nil {
		return task.Resource{}, fmt.Errorf("acknowledge task: %w", err)
	}
	if err := insertTaskEvent(ctx, transaction, taskID, task.PhaseRunning, "acknowledged", acknowledgedAt); err != nil {
		return task.Resource{}, err
	}
	if resource.Kind == task.KindOperationPlanningTask {
		if _, err := transaction.ExecContext(ctx, `UPDATE operation_runs SET state = ?, checkpoint = ?, updated_at = ? WHERE task_id = ?`,
			operation.StateDiscovering, "discovering", timestamp, taskID); err != nil {
			return task.Resource{}, fmt.Errorf("advance operation run after planning acknowledgement: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return task.Resource{}, fmt.Errorf("commit task acknowledgement: %w", err)
	}
	return store.GetTask(ctx, taskID)
}

func (store *Store) CancelTask(ctx context.Context, taskID string, canceledAt time.Time) (task.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Resource{}, fmt.Errorf("begin task cancellation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	resource, err := scanTask(transaction.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return task.Resource{}, err
	}
	if task.Terminal(resource.Status.Phase) || resource.Status.Phase == task.PhaseCancelRequested {
		return resource, nil
	}
	timestamp := formatTime(canceledAt)
	nextPhase := task.PhaseCancelRequested
	completedAt := any(nil)
	eventType := "cancellation_requested"
	if resource.Status.Phase == task.PhasePending {
		nextPhase = task.PhaseCanceled
		completedAt = timestamp
		eventType = "canceled_before_claim"
	} else if resource.Status.Phase != task.PhaseClaimed && resource.Status.Phase != task.PhaseRunning {
		return task.Resource{}, fmt.Errorf("%w: cannot cancel task in phase %s", task.ErrInvalidTransition, resource.Status.Phase)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE tasks SET phase = ?, cancel_requested_at = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
		nextPhase, timestamp, completedAt, timestamp, taskID); err != nil {
		return task.Resource{}, fmt.Errorf("cancel task: %w", err)
	}
	if err := insertTaskEvent(ctx, transaction, taskID, nextPhase, eventType, canceledAt); err != nil {
		return task.Resource{}, err
	}
	if err := transaction.Commit(); err != nil {
		return task.Resource{}, fmt.Errorf("commit task cancellation: %w", err)
	}
	return store.GetTask(ctx, taskID)
}

func (store *Store) CompleteTask(
	ctx context.Context,
	agentID, taskID string,
	submission task.ResultSubmission,
	reportedAt time.Time,
) (task.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Resource{}, fmt.Errorf("begin task completion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	resource, err := getTaskForAgent(ctx, transaction, agentID, taskID, submission.ClaimID)
	if err != nil {
		return task.Resource{}, err
	}
	resultJSON, failure, err := encodeTaskSubmission(resource, submission)
	if err != nil {
		return task.Resource{}, err
	}
	digest := sha256.Sum256(resultJSON)
	if task.Terminal(resource.Status.Phase) {
		var existingDigest []byte
		if err := transaction.QueryRowContext(ctx,
			`SELECT result_digest FROM task_results WHERE task_id = ?`, taskID).Scan(&existingDigest); err != nil {
			if errors.Is(err, sql.ErrNoRows) && resource.Status.Phase == task.PhaseCanceled {
				return resource, nil
			}
			return task.Resource{}, fmt.Errorf("read existing task result: %w", err)
		}
		if string(existingDigest) != string(digest[:]) || resource.Status.Phase != submission.Phase {
			return task.Resource{}, task.ErrResultConflict
		}
		return resource, nil
	}
	if resource.Kind == task.KindOperationPlanningTask && resource.Status.Phase == task.PhaseCancelRequested && submission.Phase != task.PhaseCanceled {
		return task.Resource{}, fmt.Errorf("%w: cancel-requested operation task only accepts a canceled result", task.ErrInvalidTransition)
	}
	if resource.Status.Phase != task.PhaseClaimed && resource.Status.Phase != task.PhaseRunning && resource.Status.Phase != task.PhaseCancelRequested {
		return task.Resource{}, fmt.Errorf("%w: cannot complete task in phase %s", task.ErrInvalidTransition, resource.Status.Phase)
	}
	var errorCode, errorMessage any
	if failure != nil {
		errorCode = failure.Code
		errorMessage = failure.Message
	}
	timestamp := formatTime(reportedAt)
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO task_results(task_id, result_json, result_digest, reported_at) VALUES(?, ?, ?, ?)`,
		taskID, string(resultJSON), digest[:], timestamp); err != nil {
		return task.Resource{}, fmt.Errorf("insert task result: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE tasks SET phase = ?, completed_at = ?, updated_at = ?, last_error_code = ?, last_error_message = ?
		WHERE id = ?`, submission.Phase, timestamp, timestamp, errorCode, errorMessage, taskID); err != nil {
		return task.Resource{}, fmt.Errorf("complete task: %w", err)
	}
	if err := insertTaskEvent(ctx, transaction, taskID, submission.Phase, "result_reported", reportedAt); err != nil {
		return task.Resource{}, err
	}
	if submission.OperationResult != nil {
		if err := applyOperationPlanningResultTx(ctx, transaction, taskID, *submission.OperationResult, reportedAt); err != nil {
			return task.Resource{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return task.Resource{}, fmt.Errorf("commit task completion: %w", err)
	}
	return store.GetTask(ctx, taskID)
}

func getTaskByIdempotencyKey(ctx context.Context, source rowQueryer, key string) (task.Resource, error) {
	resource, err := scanTask(source.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Resource{}, domain.ErrNotFound
	}
	return resource, err
}

func getTaskForAgent(ctx context.Context, transaction *sql.Tx, agentID, taskID, claimID string) (task.Resource, error) {
	resource, err := scanTask(transaction.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return task.Resource{}, err
	}
	if resource.Spec.NodeID != agentID {
		return task.Resource{}, task.ErrNodeMismatch
	}
	if resource.Status.ClaimID == "" || resource.Status.ClaimID != claimID {
		return task.Resource{}, task.ErrClaimMismatch
	}
	return resource, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanTask(source scanner) (task.Resource, error) {
	resource := task.Resource{APIVersion: "setpoint.io/v1"}
	var parameters, targets, secretRefs, executionContract, contractDigest, phase, createdAt, updatedAt string
	var pluginID sql.NullString
	var claimID, claimedAt, acknowledgedAt, cancelRequestedAt, completedAt sql.NullString
	var lastErrorCode, lastErrorMessage sql.NullString
	if err := source.Scan(
		&resource.Metadata.ID, &resource.Metadata.IdempotencyKey, &resource.Spec.NodeID, &pluginID,
		&resource.Kind, &resource.Spec.OperationID, &resource.Spec.OperationVersion, &resource.Spec.CapabilityDigest,
		&targets, &secretRefs, &parameters, &executionContract, &contractDigest,
		&phase, &claimID, &resource.Status.Attempt, &createdAt, &updatedAt,
		&claimedAt, &acknowledgedAt, &cancelRequestedAt, &completedAt, &lastErrorCode, &lastErrorMessage,
	); err != nil {
		return task.Resource{}, err
	}
	resource.Spec.Parameters = json.RawMessage(parameters)
	resource.Spec.PluginID = pluginID.String
	resource.Spec.ContractDigest = contractDigest
	if executionContract != "" {
		var contract task.CheckExecutionContract
		if err := json.Unmarshal([]byte(executionContract), &contract); err != nil {
			return task.Resource{}, fmt.Errorf("decode task execution contract: %w", err)
		}
		if err := task.ValidateCheckExecutionContract(contract, contractDigest); err != nil {
			return task.Resource{}, fmt.Errorf("validate task execution contract: %w", err)
		}
		resource.Spec.Execution = &contract
	}
	if err := json.Unmarshal([]byte(targets), &resource.Spec.Targets); err != nil {
		return task.Resource{}, fmt.Errorf("decode task targets: %w", err)
	}
	if err := json.Unmarshal([]byte(secretRefs), &resource.Spec.SecretRefs); err != nil {
		return task.Resource{}, fmt.Errorf("decode task secret refs: %w", err)
	}
	resource.Status.Phase = task.Phase(phase)
	resource.Status.ClaimID = claimID.String
	var err error
	resource.Metadata.CreatedAt, err = parseTime(createdAt, "task creation")
	if err != nil {
		return task.Resource{}, err
	}
	resource.Status.UpdatedAt, err = parseTime(updatedAt, "task update")
	if err != nil {
		return task.Resource{}, err
	}
	for value, field := range map[string]struct {
		input  sql.NullString
		target **time.Time
	}{
		"task claim":                {claimedAt, &resource.Status.ClaimedAt},
		"task acknowledgement":      {acknowledgedAt, &resource.Status.AcknowledgedAt},
		"task cancellation request": {cancelRequestedAt, &resource.Status.CancelRequestedAt},
		"task completion":           {completedAt, &resource.Status.CompletedAt},
	} {
		if !field.input.Valid {
			continue
		}
		parsed, err := parseTime(field.input.String, value)
		if err != nil {
			return task.Resource{}, err
		}
		*field.target = &parsed
	}
	if lastErrorCode.Valid || lastErrorMessage.Valid {
		resource.Status.LastError = &task.Failure{Code: lastErrorCode.String, Message: lastErrorMessage.String}
	}
	return resource, nil
}

type taskResultQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadTaskResult(ctx context.Context, source taskResultQueryer, resource *task.Resource) error {
	var raw string
	err := source.QueryRowContext(ctx, `SELECT result_json FROM task_results WHERE task_id = ?`, resource.Metadata.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read task result: %w", err)
	}
	switch resource.Kind {
	case task.KindReadOnlyCheckTask:
		var result task.CheckResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return fmt.Errorf("decode check task result: %w", err)
		}
		for index := range result.Items {
			task.NormalizeItem(&result.Items[index])
		}
		resource.Result = &result
	case task.KindOperationPlanningTask:
		var result operation.PlanningResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return fmt.Errorf("decode operation planning task result: %w", err)
		}
		resource.OperationResult = &result
	default:
		return fmt.Errorf("decode result for unsupported task kind %q", resource.Kind)
	}
	return nil
}

func sameTaskSpec(left, right task.Resource) bool {
	leftTargets, _ := json.Marshal(left.Spec.Targets)
	rightTargets, _ := json.Marshal(right.Spec.Targets)
	leftRefs, _ := json.Marshal(left.Spec.SecretRefs)
	rightRefs, _ := json.Marshal(right.Spec.SecretRefs)
	return left.Kind == right.Kind && left.Spec.NodeID == right.Spec.NodeID &&
		left.Spec.PluginID == right.Spec.PluginID && left.Spec.OperationID == right.Spec.OperationID &&
		left.Spec.OperationVersion == right.Spec.OperationVersion && left.Spec.CapabilityDigest == right.Spec.CapabilityDigest &&
		left.Spec.ContractDigest == right.Spec.ContractDigest &&
		string(leftTargets) == string(rightTargets) && string(leftRefs) == string(rightRefs) &&
		string(left.Spec.Parameters) == string(right.Spec.Parameters)
}

func insertTask(ctx context.Context, transaction *sql.Tx, resource task.Resource) error {
	executionContract, contractDigest, err := encodeTaskExecution(resource.Spec)
	if err != nil {
		return err
	}
	targets, err := json.Marshal(resource.Spec.Targets)
	if err != nil {
		return fmt.Errorf("encode task targets: %w", err)
	}
	secretRefs, err := json.Marshal(resource.Spec.SecretRefs)
	if err != nil {
		return fmt.Errorf("encode task secret refs: %w", err)
	}
	timestamp := formatTime(resource.Metadata.CreatedAt)
	var pluginID any
	if resource.Kind == task.KindReadOnlyCheckTask {
		pluginID = resource.Spec.PluginID
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO tasks(id, idempotency_key, node_id, plugin_id, kind, operation_id, operation_version,
			capability_digest, targets_json, secret_refs_json, parameters_json,
			execution_contract_json, execution_contract_digest, phase, attempt, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		resource.Metadata.ID, resource.Metadata.IdempotencyKey, resource.Spec.NodeID, pluginID,
		resource.Kind, resource.Spec.OperationID, resource.Spec.OperationVersion, resource.Spec.CapabilityDigest,
		string(targets), string(secretRefs), string(resource.Spec.Parameters), executionContract, contractDigest,
		task.PhasePending, timestamp, timestamp)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return insertTaskEvent(ctx, transaction, resource.Metadata.ID, task.PhasePending, "created", resource.Metadata.CreatedAt)
}

func encodeTaskExecution(spec task.Spec) (string, string, error) {
	if spec.Execution == nil {
		if spec.ContractDigest != "" {
			return "", "", errors.New("task contract digest requires an execution contract")
		}
		return "", "", nil
	}
	if err := task.ValidateCheckExecutionContract(*spec.Execution, spec.ContractDigest); err != nil {
		return "", "", fmt.Errorf("validate task execution contract: %w", err)
	}
	encoded, err := json.Marshal(spec.Execution)
	if err != nil {
		return "", "", fmt.Errorf("encode task execution contract: %w", err)
	}
	return string(encoded), spec.ContractDigest, nil
}

func encodeTaskSubmission(resource task.Resource, submission task.ResultSubmission) ([]byte, *task.Failure, error) {
	switch resource.Kind {
	case task.KindReadOnlyCheckTask:
		if submission.Result == nil || submission.OperationResult != nil {
			return nil, nil, errors.New("check task requires exactly one check result")
		}
		encoded, err := json.Marshal(submission.Result)
		return encoded, submission.Result.Error, err
	case task.KindOperationPlanningTask:
		if submission.OperationResult == nil || submission.Result != nil {
			return nil, nil, errors.New("operation planning task requires exactly one operation result")
		}
		encoded, err := json.Marshal(submission.OperationResult)
		var failure *task.Failure
		if submission.OperationResult.Error != nil {
			failure = &task.Failure{Code: submission.OperationResult.Error.Code, Message: submission.OperationResult.Error.Message}
		}
		return encoded, failure, err
	default:
		return nil, nil, fmt.Errorf("unsupported task kind %q", resource.Kind)
	}
}

func insertTaskEvent(ctx context.Context, transaction *sql.Tx, taskID string, phase task.Phase, eventType string, at time.Time) error {
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO task_events(task_id, phase, event_type, created_at, details_json)
		VALUES(?, ?, ?, ?, '{}')`, taskID, phase, eventType, formatTime(at)); err != nil {
		return fmt.Errorf("record task event: %w", err)
	}
	return nil
}
