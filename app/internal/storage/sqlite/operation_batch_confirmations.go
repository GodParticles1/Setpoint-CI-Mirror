package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operationbatch"
)

const operationBatchConfirmationColumns = `batch_id, source_check_run_id, confirmation_fingerprint, confirmation_idempotency_key, accepted_at`

func (store *Store) CreateOrGetOperationBatchConfirmation(ctx context.Context, receipt operationbatch.Receipt) (operationbatch.Receipt, bool, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return operationbatch.Receipt{}, false, fmt.Errorf("begin operation batch confirmation creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	if existing, found, err := readOperationBatchConfirmationByConfirmationKeyTx(ctx, transaction, receipt.ConfirmationIdempotencyKey); err != nil {
		return operationbatch.Receipt{}, false, err
	} else if found {
		if !sameBatchConfirmation(existing, receipt) {
			return operationbatch.Receipt{}, false, operationbatch.ErrFingerprintConflict
		}
		return existing, false, nil
	}
	if existing, found, err := readOperationBatchConfirmationByIDTx(ctx, transaction, receipt.BatchID); err != nil {
		return operationbatch.Receipt{}, false, err
	} else if found {
		if existing.ConfirmationFingerprint != receipt.ConfirmationFingerprint || existing.ConfirmationIdempotencyKey != receipt.ConfirmationIdempotencyKey {
			return operationbatch.Receipt{}, false, operationbatch.ErrFingerprintConflict
		}
		return operationbatch.Receipt{}, false, errors.New("batch confirmation exists without confirmation-key lookup consistency")
	}

	if _, err := transaction.ExecContext(ctx, `INSERT INTO operation_batch_confirmations(
		batch_id, source_check_run_id, confirmation_fingerprint, confirmation_idempotency_key, accepted_at
	) VALUES(?, ?, ?, ?, ?)`, receipt.BatchID, receipt.SourceCheckRunID, receipt.ConfirmationFingerprint,
		receipt.ConfirmationIdempotencyKey, formatTime(receipt.AcceptedAt)); err != nil {
		return operationbatch.Receipt{}, false, fmt.Errorf("insert operation batch confirmation: %w", err)
	}
	for index, member := range receipt.Members {
		if member.Ordinal != index {
			return operationbatch.Receipt{}, false, errors.New("operation batch confirmation members must use contiguous ordered ordinals")
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO operation_batch_confirmation_members(
			batch_id, ordinal, task_id, check_id, node_id, run_id, plan_digest, fanout_state, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, receipt.BatchID, member.Ordinal, member.Identity.TaskID,
			member.Identity.CheckID, member.Identity.NodeID, member.RunID, member.PlanDigest, member.State,
			formatTime(member.UpdatedAt)); err != nil {
			return operationbatch.Receipt{}, false, fmt.Errorf("%w: insert operation batch confirmation member %d: %v", operationbatch.ErrFingerprintConflict, index, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return operationbatch.Receipt{}, false, fmt.Errorf("commit operation batch confirmation: %w", err)
	}
	created, err := store.GetOperationBatchConfirmation(ctx, receipt.BatchID)
	return created, true, err
}

func (store *Store) GetOperationBatchConfirmation(ctx context.Context, batchID string) (operationbatch.Receipt, error) {
	receipt, found, err := readOperationBatchConfirmationHeader(ctx, store.db, `WHERE batch_id = ?`, batchID)
	if err != nil {
		return operationbatch.Receipt{}, err
	}
	if !found {
		return operationbatch.Receipt{}, domain.ErrNotFound
	}
	receipt.Members, err = readOperationBatchConfirmationMembers(ctx, store.db, receipt.BatchID)
	return receipt, err
}

func (store *Store) GetOperationBatchConfirmationByKey(ctx context.Context, confirmationKey string) (operationbatch.Receipt, error) {
	receipt, found, err := readOperationBatchConfirmationHeader(ctx, store.db, `WHERE confirmation_idempotency_key = ?`, confirmationKey)
	if err != nil {
		return operationbatch.Receipt{}, err
	}
	if !found {
		return operationbatch.Receipt{}, domain.ErrNotFound
	}
	receipt.Members, err = readOperationBatchConfirmationMembers(ctx, store.db, receipt.BatchID)
	return receipt, err
}

func (store *Store) ListOperationBatchConfirmationsByCheckRun(ctx context.Context, checkRunID string, limit, offset int) ([]operationbatch.Receipt, error) {
	return store.listOperationBatchConfirmations(ctx, `WHERE source_check_run_id = ? ORDER BY accepted_at DESC, batch_id DESC LIMIT ? OFFSET ?`, checkRunID, limit, offset)
}

func (store *Store) ListPendingOperationBatchConfirmations(ctx context.Context, limit, offset int) ([]operationbatch.Receipt, error) {
	return store.listOperationBatchConfirmations(ctx, `WHERE EXISTS (
		SELECT 1 FROM operation_batch_confirmation_members m WHERE m.batch_id = operation_batch_confirmations.batch_id AND m.fanout_state = 'pending'
	) ORDER BY accepted_at ASC, batch_id ASC LIMIT ? OFFSET ?`, limit, offset)
}

func (store *Store) listOperationBatchConfirmations(ctx context.Context, suffix string, args ...any) ([]operationbatch.Receipt, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+operationBatchConfirmationColumns+` FROM operation_batch_confirmations `+suffix, args...)
	if err != nil {
		return nil, fmt.Errorf("list operation batch confirmations: %w", err)
	}
	receipts := make([]operationbatch.Receipt, 0)
	for rows.Next() {
		receipt, err := scanOperationBatchConfirmation(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate operation batch confirmations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// Store uses a single SQLite connection. Read member rows only after the
	// outer result set is closed so reconstruction cannot self-block.
	for index := range receipts {
		members, err := readOperationBatchConfirmationMembers(ctx, store.db, receipts[index].BatchID)
		if err != nil {
			return nil, err
		}
		receipts[index].Members = members
	}
	return receipts, nil
}

func (store *Store) UpdateOperationBatchConfirmationMemberState(ctx context.Context, batchID string, ordinal int, state operationbatch.MemberState, at time.Time) (operationbatch.Receipt, error) {
	if state != operationbatch.MemberConfirmed && state != operationbatch.MemberSuppressedCanceled {
		return operationbatch.Receipt{}, errors.New("operation batch confirmation member may only converge from pending to a terminal fan-out fact")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE operation_batch_confirmation_members SET fanout_state = ?, updated_at = ?
		WHERE batch_id = ? AND ordinal = ? AND fanout_state = 'pending'`, state, formatTime(at.UTC()), batchID, ordinal)
	if err != nil {
		return operationbatch.Receipt{}, fmt.Errorf("update operation batch confirmation member: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return operationbatch.Receipt{}, err
	} else if count == 0 {
		var current string
		if err := store.db.QueryRowContext(ctx, `SELECT fanout_state FROM operation_batch_confirmation_members WHERE batch_id = ? AND ordinal = ?`, batchID, ordinal).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return operationbatch.Receipt{}, domain.ErrNotFound
			}
			return operationbatch.Receipt{}, err
		}
		if operationbatch.MemberState(current) != state {
			return operationbatch.Receipt{}, errors.New("operation batch confirmation member fan-out fact is immutable once terminal")
		}
	}
	return store.GetOperationBatchConfirmation(ctx, batchID)
}

func readOperationBatchConfirmationByConfirmationKeyTx(ctx context.Context, transaction *sql.Tx, key string) (operationbatch.Receipt, bool, error) {
	receipt, found, err := readOperationBatchConfirmationHeaderTxWhere(ctx, transaction, `WHERE confirmation_idempotency_key = ?`, key)
	if err != nil || !found {
		return receipt, found, err
	}
	members, err := readOperationBatchConfirmationMembersTx(ctx, transaction, receipt.BatchID)
	if err != nil {
		return operationbatch.Receipt{}, false, err
	}
	receipt.Members = members
	return receipt, true, nil
}

func readOperationBatchConfirmationByIDTx(ctx context.Context, transaction *sql.Tx, batchID string) (operationbatch.Receipt, bool, error) {
	return readOperationBatchConfirmationHeaderTxWhere(ctx, transaction, `WHERE batch_id = ?`, batchID)
}

func readOperationBatchConfirmationHeaderTxWhere(ctx context.Context, transaction *sql.Tx, suffix string, args ...any) (operationbatch.Receipt, bool, error) {
	receipt, err := scanOperationBatchConfirmation(transaction.QueryRowContext(ctx, `SELECT `+operationBatchConfirmationColumns+` FROM operation_batch_confirmations `+suffix, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return operationbatch.Receipt{}, false, nil
	}
	return receipt, err == nil, err
}

func readOperationBatchConfirmationHeader(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, suffix string, args ...any) (operationbatch.Receipt, bool, error) {
	receipt, err := scanOperationBatchConfirmation(queryer.QueryRowContext(ctx, `SELECT `+operationBatchConfirmationColumns+` FROM operation_batch_confirmations `+suffix, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return operationbatch.Receipt{}, false, nil
	}
	return receipt, err == nil, err
}

func scanOperationBatchConfirmation(source scanner) (operationbatch.Receipt, error) {
	receipt := operationbatch.Receipt{APIVersion: operationbatch.APIVersion, Kind: operationbatch.Kind}
	var acceptedAt string
	if err := source.Scan(&receipt.BatchID, &receipt.SourceCheckRunID, &receipt.ConfirmationFingerprint,
		&receipt.ConfirmationIdempotencyKey, &acceptedAt); err != nil {
		return operationbatch.Receipt{}, err
	}
	var err error
	receipt.AcceptedAt, err = parseTime(acceptedAt, "operation batch confirmation acceptance")
	return receipt, err
}

func readOperationBatchConfirmationMembers(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, batchID string) ([]operationbatch.Member, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT ordinal, task_id, check_id, node_id, run_id, plan_digest, fanout_state, updated_at
		FROM operation_batch_confirmation_members WHERE batch_id = ? ORDER BY ordinal ASC`, batchID)
	if err != nil {
		return nil, fmt.Errorf("list operation batch confirmation members: %w", err)
	}
	defer rows.Close()
	return scanOperationBatchConfirmationMembers(rows)
}

func readOperationBatchConfirmationMembersTx(ctx context.Context, transaction *sql.Tx, batchID string) ([]operationbatch.Member, error) {
	return readOperationBatchConfirmationMembers(ctx, transaction, batchID)
}

func scanOperationBatchConfirmationMembers(rows *sql.Rows) ([]operationbatch.Member, error) {
	members := make([]operationbatch.Member, 0)
	for rows.Next() {
		var member operationbatch.Member
		var state, updatedAt string
		if err := rows.Scan(&member.Ordinal, &member.Identity.TaskID, &member.Identity.CheckID, &member.Identity.NodeID,
			&member.RunID, &member.PlanDigest, &state, &updatedAt); err != nil {
			return nil, err
		}
		member.State = operationbatch.MemberState(state)
		parsed, err := parseTime(updatedAt, "operation batch confirmation member update")
		if err != nil {
			return nil, err
		}
		member.UpdatedAt = parsed
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index, member := range members {
		if member.Ordinal != index {
			return nil, errors.New("persisted operation batch confirmation member ordinals are not contiguous")
		}
	}
	return members, nil
}

func sameBatchConfirmation(left, right operationbatch.Receipt) bool {
	return left.BatchID == right.BatchID && left.SourceCheckRunID == right.SourceCheckRunID &&
		left.ConfirmationFingerprint == right.ConfirmationFingerprint &&
		left.ConfirmationIdempotencyKey == right.ConfirmationIdempotencyKey
}
