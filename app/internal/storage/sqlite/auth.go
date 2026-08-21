package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/auth"
	"setpoint/internal/domain"
)

func (store *Store) CreateEnrollmentToken(ctx context.Context, record auth.EnrollmentRecord) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO enrollment_tokens(id, secret_digest, expires_at, max_uses, created_at)
		VALUES(?, ?, ?, ?, ?)`, record.ID, record.Digest, formatTime(record.ExpiresAt), record.MaxUses, formatTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("create enrollment token: %w", err)
	}
	return nil
}

func (store *Store) EnrollAgentCredential(
	ctx context.Context,
	presented auth.PresentedToken,
	credential auth.CredentialRecord,
	usedAt time.Time,
) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Agent enrollment: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var digest []byte
	var expiresAt string
	var maxUses, uses int
	var revokedAt sql.NullString
	err = transaction.QueryRowContext(ctx, `
		SELECT secret_digest, expires_at, max_uses, uses, revoked_at
		FROM enrollment_tokens WHERE id = ?`, presented.ID).Scan(&digest, &expiresAt, &maxUses, &uses, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &auth.Error{Code: auth.CodeInvalid}
	}
	if err != nil {
		return fmt.Errorf("read enrollment token: %w", err)
	}
	if !auth.DigestMatches(presented.Digest, digest) {
		return &auth.Error{Code: auth.CodeInvalid}
	}
	if revokedAt.Valid {
		return &auth.Error{Code: auth.CodeEnrollmentTokenRevoked}
	}
	expiry, err := parseTime(expiresAt, "enrollment token expiry")
	if err != nil {
		return err
	}
	if !usedAt.Before(expiry) {
		return &auth.Error{Code: auth.CodeEnrollmentTokenExpired}
	}
	if uses >= maxUses {
		return &auth.Error{Code: auth.CodeEnrollmentTokenExhausted}
	}
	if err := insertCredential(ctx, transaction, credential); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE enrollment_tokens SET uses = uses + 1, last_used_at = ? WHERE id = ?`,
		formatTime(usedAt), presented.ID); err != nil {
		return fmt.Errorf("consume enrollment token: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit Agent enrollment: %w", err)
	}
	return nil
}

func (store *Store) AuthenticateAgentCredential(
	ctx context.Context,
	presented auth.PresentedToken,
	agentID string,
	usedAt time.Time,
) (auth.Credential, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return auth.Credential{}, fmt.Errorf("begin Agent authentication: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	credential, err := authenticateCredential(ctx, transaction, presented, agentID, usedAt)
	if err != nil {
		return auth.Credential{}, err
	}
	if err := transaction.Commit(); err != nil {
		return auth.Credential{}, fmt.Errorf("commit Agent authentication: %w", err)
	}
	return credential, nil
}

func (store *Store) RotateAgentCredential(
	ctx context.Context,
	presented auth.PresentedToken,
	agentID string,
	replacement auth.CredentialRecord,
	rotatedAt time.Time,
) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Agent credential rotation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	current, err := authenticateCredential(ctx, transaction, presented, agentID, rotatedAt)
	if err != nil {
		return err
	}
	if replacement.RotatedFrom != current.ID {
		return errors.New("replacement credential rotation lineage does not match current credential")
	}
	if replacement.AgentID != current.AgentID {
		return errors.New("replacement credential Agent does not match current credential")
	}
	if err := insertCredential(ctx, transaction, replacement); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit Agent credential rotation: %w", err)
	}
	return nil
}

func (store *Store) RevokeEnrollmentToken(ctx context.Context, id string, revokedAt time.Time) error {
	return revokeRecord(ctx, store.db, "enrollment_tokens", id, revokedAt)
}

func (store *Store) RevokeAgentCredential(ctx context.Context, id string, revokedAt time.Time) error {
	result, err := store.db.ExecContext(ctx, `
		WITH RECURSIVE credential_lineage(id) AS (
			SELECT id FROM agent_credentials WHERE id = ?
			UNION ALL
			SELECT child.id FROM agent_credentials child
			JOIN credential_lineage parent ON child.rotated_from = parent.id
		)
		UPDATE agent_credentials
		SET revoked_at = COALESCE(revoked_at, ?)
		WHERE id IN (SELECT id FROM credential_lineage)`, id, formatTime(revokedAt))
	if err != nil {
		return fmt.Errorf("revoke Agent credential lineage: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Agent credential revocation count: %w", err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func authenticateCredential(
	ctx context.Context,
	transaction *sql.Tx,
	presented auth.PresentedToken,
	agentID string,
	usedAt time.Time,
) (auth.Credential, error) {
	var credential auth.Credential
	var digest []byte
	var createdAt string
	var expiresAt, revokedAt, rotatedFrom sql.NullString
	err := transaction.QueryRowContext(ctx, `
		SELECT id, agent_id, secret_digest, created_at, expires_at, revoked_at, rotated_from
		FROM agent_credentials WHERE id = ?`, presented.ID).Scan(
		&credential.ID, &credential.AgentID, &digest, &createdAt, &expiresAt, &revokedAt, &rotatedFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Credential{}, &auth.Error{Code: auth.CodeInvalid}
	}
	if err != nil {
		return auth.Credential{}, fmt.Errorf("read Agent credential: %w", err)
	}
	if !auth.DigestMatches(presented.Digest, digest) {
		return auth.Credential{}, &auth.Error{Code: auth.CodeInvalid}
	}
	if credential.AgentID != agentID {
		return auth.Credential{}, &auth.Error{Code: auth.CodeAgentMismatch}
	}
	credential.CreatedAt, err = parseTime(createdAt, "Agent credential creation")
	if err != nil {
		return auth.Credential{}, err
	}
	if expiresAt.Valid {
		expiry, err := parseTime(expiresAt.String, "Agent credential expiry")
		if err != nil {
			return auth.Credential{}, err
		}
		credential.ExpiresAt = &expiry
		if !usedAt.Before(expiry) {
			return auth.Credential{}, &auth.Error{Code: auth.CodeExpired}
		}
	}
	if revokedAt.Valid {
		revocation, err := parseTime(revokedAt.String, "Agent credential revocation")
		if err != nil {
			return auth.Credential{}, err
		}
		credential.RevokedAt = &revocation
		return auth.Credential{}, &auth.Error{Code: auth.CodeRevoked}
	}
	credential.RotatedFrom = rotatedFrom.String
	if _, err := transaction.ExecContext(ctx,
		`UPDATE agent_credentials SET last_used_at = ? WHERE id = ?`, formatTime(usedAt), credential.ID); err != nil {
		return auth.Credential{}, fmt.Errorf("record Agent credential use: %w", err)
	}
	if credential.RotatedFrom != "" {
		if _, err := transaction.ExecContext(ctx, `
			UPDATE agent_credentials SET revoked_at = COALESCE(revoked_at, ?)
			WHERE id = ? AND agent_id = ?`,
			formatTime(usedAt), credential.RotatedFrom, credential.AgentID); err != nil {
			return auth.Credential{}, fmt.Errorf("activate rotated Agent credential: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE agent_credentials SET revoked_at = COALESCE(revoked_at, ?)
			WHERE rotated_from = ? AND id <> ? AND agent_id = ?`,
			formatTime(usedAt), credential.RotatedFrom, credential.ID, credential.AgentID); err != nil {
			return auth.Credential{}, fmt.Errorf("revoke superseded Agent credential: %w", err)
		}
	}
	return credential, nil
}

func insertCredential(ctx context.Context, transaction *sql.Tx, credential auth.CredentialRecord) error {
	var expiresAt any
	if credential.ExpiresAt != nil {
		expiresAt = formatTime(*credential.ExpiresAt)
	}
	var rotatedFrom any
	if credential.RotatedFrom != "" {
		rotatedFrom = credential.RotatedFrom
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO agent_credentials(id, agent_id, secret_digest, created_at, expires_at, rotated_from)
		VALUES(?, ?, ?, ?, ?, ?)`, credential.ID, credential.AgentID, credential.Digest,
		formatTime(credential.CreatedAt), expiresAt, rotatedFrom); err != nil {
		return fmt.Errorf("create Agent credential: %w", err)
	}
	return nil
}

func revokeRecord(ctx context.Context, database *sql.DB, table, id string, revokedAt time.Time) error {
	query := "UPDATE " + table + " SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?"
	result, err := database.ExecContext(ctx, query, formatTime(revokedAt), id)
	if err != nil {
		return fmt.Errorf("revoke %s record: %w", table, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read %s revocation count: %w", table, err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(timeFormat)
}

func parseTime(value, field string) (time.Time, error) {
	parsed, err := time.Parse(timeFormat, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", field, err)
	}
	return parsed, nil
}
