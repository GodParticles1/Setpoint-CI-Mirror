package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/task"
)

const operationRunColumns = `id, idempotency_key, operation_id, operation_version, capability_digest,
	node_id, targets_json, parameters_json, secret_refs_json, state, checkpoint, task_id, plan_digest,
	discovery_json, precheck_json, plan_json, impact_json, block_json, recovery_json, execution_json, created_at, updated_at`

func (store *Store) CreateOperationRun(
	ctx context.Context,
	run operationrun.Resource,
	planningTask task.Resource,
) (operationrun.Resource, bool, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operationrun.Resource{}, false, fmt.Errorf("begin operation run creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	existing, err := scanOperationRun(transaction.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE idempotency_key = ?`, run.Metadata.IdempotencyKey))
	if err == nil {
		if !sameOperationRunSpec(existing, run) {
			return operationrun.Resource{}, false, domain.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return operationrun.Resource{}, false, err
	}
	if err := insertTask(ctx, transaction, planningTask); err != nil {
		return operationrun.Resource{}, false, err
	}
	targets, parameters, secretRefs, err := encodeOperationRunSpec(run.Spec)
	if err != nil {
		return operationrun.Resource{}, false, err
	}
	createdAt := formatTime(run.Metadata.CreatedAt)
	updatedAt := formatTime(run.Status.UpdatedAt)
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO operation_runs(id, idempotency_key, operation_id, operation_version, capability_digest,
			node_id, targets_json, parameters_json, secret_refs_json, state, checkpoint, task_id, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.Metadata.ID, run.Metadata.IdempotencyKey, run.Spec.OperationID, run.Spec.OperationVersion,
		run.Spec.CapabilityDigest, run.Spec.NodeID, targets, parameters, secretRefs,
		run.Status.State, run.Status.Checkpoint, run.Status.TaskID, createdAt, updatedAt)
	if err != nil {
		return operationrun.Resource{}, false, fmt.Errorf("insert operation run: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return operationrun.Resource{}, false, fmt.Errorf("commit operation run creation: %w", err)
	}
	created, err := store.GetOperationRun(ctx, run.Metadata.ID)
	return created, true, err
}

func (store *Store) GetOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	run, err := scanOperationRun(store.db.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return operationrun.Resource{}, domain.ErrNotFound
	}
	return run, err
}

func (store *Store) ListOperationRuns(ctx context.Context, limit, offset int) ([]operationrun.Resource, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+operationRunColumns+`
		FROM operation_runs ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list operation runs: %w", err)
	}
	defer rows.Close()
	runs := make([]operationrun.Resource, 0)
	for rows.Next() {
		run, err := scanOperationRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate operation runs: %w", err)
	}
	return runs, nil
}

func (store *Store) CancelOperationRun(ctx context.Context, runID string, at time.Time) (operationrun.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operationrun.Resource{}, fmt.Errorf("begin operation run cancellation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	run, err := scanOperationRun(transaction.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return operationrun.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return operationrun.Resource{}, err
	}
	if run.Status.State == operation.StateCanceledBeforeApply {
		return run, nil
	}
	if !operation.CanTransition(run.Status.State, operation.StateCanceledBeforeApply) {
		return operationrun.Resource{}, fmt.Errorf("%w: operation run cannot be canceled in state %s", task.ErrInvalidTransition, run.Status.State)
	}
	resource, err := scanTask(transaction.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, run.Status.TaskID))
	if err != nil {
		return operationrun.Resource{}, err
	}
	timestamp := formatTime(at)
	if !task.Terminal(resource.Status.Phase) && resource.Status.Phase != task.PhaseCancelRequested {
		nextPhase := task.PhaseCancelRequested
		completedAt := any(nil)
		eventType := "cancellation_requested"
		if resource.Status.Phase == task.PhasePending {
			nextPhase = task.PhaseCanceled
			completedAt = timestamp
			eventType = "canceled_before_claim"
		} else if resource.Status.Phase != task.PhaseClaimed && resource.Status.Phase != task.PhaseRunning {
			return operationrun.Resource{}, fmt.Errorf("%w: cannot cancel operation task in phase %s", task.ErrInvalidTransition, resource.Status.Phase)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE tasks SET phase = ?, cancel_requested_at = ?,
			completed_at = ?, updated_at = ? WHERE id = ?`, nextPhase, timestamp, completedAt, timestamp, resource.Metadata.ID); err != nil {
			return operationrun.Resource{}, fmt.Errorf("cancel operation task: %w", err)
		}
		if err := insertTaskEvent(ctx, transaction, resource.Metadata.ID, nextPhase, eventType, at); err != nil {
			return operationrun.Resource{}, err
		}
	}

	var lastSequence int64
	var lastState, lastCheckpoint string
	journalErr := transaction.QueryRowContext(ctx, `SELECT sequence, state, checkpoint FROM operation_journal WHERE run_id = ? ORDER BY sequence DESC LIMIT 1`, runID).
		Scan(&lastSequence, &lastState, &lastCheckpoint)
	switch {
	case journalErr == nil:
		if operation.State(lastState) != run.Status.State || lastCheckpoint != run.Status.Checkpoint {
			return operationrun.Resource{}, fmt.Errorf("operation cancellation refused because run state/checkpoint diverges from journal tail: run=%s/%s journal=%s/%s", run.Status.State, run.Status.Checkpoint, lastState, lastCheckpoint)
		}
		entry := operation.JournalEntry{
			RunID: runID, Sequence: lastSequence + 1, State: operation.StateCanceledBeforeApply,
			Checkpoint: "canceled_before_apply", Message: "operation canceled before apply", At: at,
		}
		if err := operation.ValidateJournalEntry(entry); err != nil {
			return operationrun.Resource{}, err
		}
		if err := appendOperationJournalEntryTx(ctx, transaction, entry); err != nil {
			return operationrun.Resource{}, err
		}
	case errors.Is(journalErr, sql.ErrNoRows):
		if executionStateRequiresJournal(run.Status.State) {
			return operationrun.Resource{}, fmt.Errorf("operation cancellation refused because execution state %s has no durable journal", run.Status.State)
		}
	default:
		return operationrun.Resource{}, fmt.Errorf("read operation journal before cancellation: %w", journalErr)
	}

	if _, err := transaction.ExecContext(ctx, `UPDATE operation_runs SET state = ?, checkpoint = ?,
		updated_at = ? WHERE id = ?`, operation.StateCanceledBeforeApply, "canceled_before_apply", timestamp, runID); err != nil {
		return operationrun.Resource{}, fmt.Errorf("cancel operation run: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return operationrun.Resource{}, fmt.Errorf("commit operation run cancellation: %w", err)
	}
	return store.GetOperationRun(ctx, runID)
}

// SaveOperationExecutionSnapshot supplements immutable execution facts for the
// run's already-persisted state/checkpoint. State or checkpoint changes must go
// through SaveOperationExecutionCheckpoint / Journal.Append so the run and
// lifecycle journal cannot diverge.
func (store *Store) SaveOperationExecutionSnapshot(
	ctx context.Context,
	runID string,
	state operation.State,
	checkpoint string,
	snapshot operationrun.ExecutionSnapshot,
	recovery *operationrun.Recovery,
	at time.Time,
) (operationrun.Resource, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operationrun.Resource{}, fmt.Errorf("begin operation execution snapshot update: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	current, err := scanOperationRun(transaction.QueryRowContext(ctx,
		`SELECT `+operationRunColumns+` FROM operation_runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return operationrun.Resource{}, domain.ErrNotFound
	}
	if err != nil {
		return operationrun.Resource{}, err
	}
	if !operation.ValidState(state) {
		return operationrun.Resource{}, fmt.Errorf("invalid operation execution state %q", state)
	}
	if state != current.Status.State || checkpoint != current.Status.Checkpoint {
		return operationrun.Resource{}, errors.New("execution snapshot cannot change operation state or checkpoint; use atomic operation checkpoint persistence")
	}

	merged, err := mergeOperationExecutionSnapshot(current.Execution, snapshot)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if err := validateOperationExecutionSnapshot(merged, at); err != nil {
		return operationrun.Resource{}, err
	}
	executionJSON, err := nullableJSON(merged)
	if err != nil {
		return operationrun.Resource{}, err
	}
	mergedRecovery := current.Status.Recovery
	if recovery != nil {
		copy := *recovery
		mergedRecovery = &copy
	}
	recoveryJSON, err := nullableJSON(mergedRecovery)
	if err != nil {
		return operationrun.Resource{}, err
	}

	if _, err := transaction.ExecContext(ctx, `UPDATE operation_runs SET execution_json = ?, recovery_json = ?, updated_at = ? WHERE id = ?`,
		executionJSON, recoveryJSON, formatTime(at), runID); err != nil {
		return operationrun.Resource{}, fmt.Errorf("update operation execution snapshot: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return operationrun.Resource{}, fmt.Errorf("commit operation execution snapshot: %w", err)
	}
	return store.GetOperationRun(ctx, runID)
}

func mergeOperationExecutionSnapshot(current *operationrun.ExecutionSnapshot, update operationrun.ExecutionSnapshot) (*operationrun.ExecutionSnapshot, error) {
	if current == nil && update.RestorePoint == nil && update.Apply == nil && update.Verification == nil && update.Rollback == nil && update.RollbackVerification == nil {
		return nil, nil
	}
	merged := operationrun.ExecutionSnapshot{}
	if current != nil {
		merged = *current
	}
	if err := mergeOperationExecutionFact(&merged.RestorePoint, update.RestorePoint, "restore point"); err != nil {
		return nil, err
	}
	if err := mergeOperationExecutionFact(&merged.Apply, update.Apply, "Apply result"); err != nil {
		return nil, err
	}
	if err := mergeOperationExecutionFact(&merged.Verification, update.Verification, "verification"); err != nil {
		return nil, err
	}
	if err := mergeOperationExecutionFact(&merged.Rollback, update.Rollback, "rollback result"); err != nil {
		return nil, err
	}
	if err := mergeOperationExecutionFact(&merged.RollbackVerification, update.RollbackVerification, "rollback verification"); err != nil {
		return nil, err
	}
	return &merged, nil
}

func mergeOperationExecutionFact[T any](current **T, update *T, name string) error {
	if update == nil {
		return nil
	}
	if *current != nil {
		if !reflect.DeepEqual(**current, *update) {
			return fmt.Errorf("durable operation %s cannot be rewritten", name)
		}
		return nil
	}
	value := *update
	*current = &value
	return nil
}

func validateOperationExecutionSnapshot(snapshot *operationrun.ExecutionSnapshot, at time.Time) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.RestorePoint != nil {
		if err := operation.ValidateRestorePoint(*snapshot.RestorePoint, at); err != nil {
			return fmt.Errorf("validate durable restore point: %w", err)
		}
	}
	if snapshot.Apply != nil && snapshot.RestorePoint == nil {
		return errors.New("durable Apply result requires a restore point")
	}
	if snapshot.Verification != nil && snapshot.Apply == nil {
		return errors.New("durable verification requires an Apply result")
	}
	if snapshot.Rollback != nil && (snapshot.RestorePoint == nil || snapshot.Apply == nil) {
		return errors.New("durable rollback requires restore point and Apply results")
	}
	if snapshot.RollbackVerification != nil && snapshot.Rollback == nil {
		return errors.New("durable rollback verification requires a rollback result")
	}
	return nil
}

func applyOperationPlanningResultTx(
	ctx context.Context,
	transaction *sql.Tx,
	taskID string,
	result operation.PlanningResult,
	at time.Time,
) error {
	discovery, err := nullableJSON(result.Discovery)
	if err != nil {
		return err
	}
	precheck, err := nullableJSON(result.Precheck)
	if err != nil {
		return err
	}
	plan, err := nullableJSON(result.Plan)
	if err != nil {
		return err
	}
	impact, err := nullableJSON(result.Impact)
	if err != nil {
		return err
	}
	blockValue := result.Block
	if blockValue == nil && result.Error != nil {
		blockValue = &operation.Block{Code: result.Error.Code, Message: result.Error.Message,
			SafeNext: "inspect_the_failure_and_create_a_new_run", ManualReview: true}
	}
	block, err := nullableJSON(blockValue)
	if err != nil {
		return err
	}
	query := `UPDATE operation_runs SET state = ?, checkpoint = ?,
		plan_digest = ?, discovery_json = ?, precheck_json = ?, plan_json = ?, impact_json = ?, block_json = ?, updated_at = ?
		WHERE task_id = ?`
	if result.State != operation.StateCanceledBeforeApply {
		query += ` AND state <> 'canceled_before_apply'`
	}
	updated, err := transaction.ExecContext(ctx, query, result.State, result.Checkpoint, result.PlanDigest,
		discovery, precheck, plan, impact, block, formatTime(at), taskID)
	if err != nil {
		return fmt.Errorf("apply operation planning result: %w", err)
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("operation run for planning task %s is missing", taskID)
	}
	return nil
}

func scanOperationRun(source scanner) (operationrun.Resource, error) {
	run := operationrun.Resource{APIVersion: "setpoint.io/v1", Kind: "OperationRun"}
	var targets, parameters, secretRefs, state, createdAt, updatedAt string
	var discovery, precheck, plan, impact, block, recovery, execution sql.NullString
	if err := source.Scan(
		&run.Metadata.ID, &run.Metadata.IdempotencyKey, &run.Spec.OperationID, &run.Spec.OperationVersion,
		&run.Spec.CapabilityDigest, &run.Spec.NodeID, &targets, &parameters, &secretRefs, &state,
		&run.Status.Checkpoint, &run.Status.TaskID, &run.PlanDigest, &discovery, &precheck, &plan, &impact,
		&block, &recovery, &execution, &createdAt, &updatedAt,
	); err != nil {
		return operationrun.Resource{}, err
	}
	run.Spec.Parameters = json.RawMessage(parameters)
	if err := json.Unmarshal([]byte(targets), &run.Spec.Targets); err != nil {
		return operationrun.Resource{}, fmt.Errorf("decode operation run targets: %w", err)
	}
	if err := json.Unmarshal([]byte(secretRefs), &run.Spec.SecretRefs); err != nil {
		return operationrun.Resource{}, fmt.Errorf("decode operation run secret refs: %w", err)
	}
	run.Status.State = operation.State(state)
	run.Status.ApplyAvailable = false
	if err := decodeNullable(discovery, &run.Discovery); err != nil {
		return operationrun.Resource{}, err
	}
	if err := decodeNullable(precheck, &run.Precheck); err != nil {
		return operationrun.Resource{}, err
	}
	if err := decodeNullable(plan, &run.Plan); err != nil {
		return operationrun.Resource{}, err
	}
	if err := decodeNullable(impact, &run.Impact); err != nil {
		return operationrun.Resource{}, err
	}
	if err := decodeNullable(block, &run.Status.Block); err != nil {
		return operationrun.Resource{}, err
	}
	if err := decodeNullable(recovery, &run.Status.Recovery); err != nil {
		return operationrun.Resource{}, err
	}
	if err := decodeNullable(execution, &run.Execution); err != nil {
		return operationrun.Resource{}, err
	}
	var err error
	run.Metadata.CreatedAt, err = parseTime(createdAt, "operation run creation")
	if err != nil {
		return operationrun.Resource{}, err
	}
	run.Status.UpdatedAt, err = parseTime(updatedAt, "operation run update")
	return run, err
}

func encodeOperationRunSpec(spec operationrun.Spec) (string, string, string, error) {
	targets, err := json.Marshal(spec.Targets)
	if err != nil {
		return "", "", "", fmt.Errorf("encode operation run targets: %w", err)
	}
	secretRefs, err := json.Marshal(spec.SecretRefs)
	if err != nil {
		return "", "", "", fmt.Errorf("encode operation run secret refs: %w", err)
	}
	return string(targets), string(spec.Parameters), string(secretRefs), nil
}

func sameOperationRunSpec(left, right operationrun.Resource) bool {
	leftTargets, leftParameters, leftRefs, leftErr := encodeOperationRunSpec(left.Spec)
	rightTargets, rightParameters, rightRefs, rightErr := encodeOperationRunSpec(right.Spec)
	return leftErr == nil && rightErr == nil && left.Spec.OperationID == right.Spec.OperationID &&
		left.Spec.OperationVersion == right.Spec.OperationVersion && left.Spec.CapabilityDigest == right.Spec.CapabilityDigest &&
		left.Spec.NodeID == right.Spec.NodeID && leftTargets == rightTargets && leftParameters == rightParameters && leftRefs == rightRefs
}

func nullableJSON(value any) (any, error) {
	if value == nil || reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil() {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode operation run artifact: %w", err)
	}
	return string(encoded), nil
}

func decodeNullable[T any](raw sql.NullString, target **T) error {
	if !raw.Valid {
		return nil
	}
	var value T
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
		return fmt.Errorf("decode operation run artifact: %w", err)
	}
	*target = &value
	return nil
}
