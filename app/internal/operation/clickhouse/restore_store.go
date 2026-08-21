package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RestoreState string

const (
	RestoreIntent         RestoreState = "intent"
	RestoreCreating       RestoreState = "creating"
	RestoreReady          RestoreState = "ready"
	RestoreCleanupPending RestoreState = "cleanup_pending"
	RestoreCleaned        RestoreState = "cleaned"
	RestoreManualReview   RestoreState = "manual_review"
)

type RestoreKey struct {
	RunID    string `json:"run_id"`
	Database string `json:"database"`
	Table    string `json:"table"`
}

type RestoreObjectIdentity struct {
	Database          string `json:"database"`
	Table             string `json:"table"`
	UUID              string `json:"uuid,omitempty"`
	Engine            string `json:"engine"`
	SchemaFingerprint string `json:"schema_fingerprint"`
}

type RestoreRecord struct {
	Key            RestoreKey            `json:"key"`
	State          RestoreState          `json:"state"`
	OwnershipToken string                `json:"ownership_token"`
	Target         RestoreObjectIdentity `json:"target"`
	Restore        RestoreObjectIdentity `json:"restore"`
	Baseline       DataFingerprint       `json:"baseline"`
	Partitions     []Partition           `json:"partitions,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	LastError      string                `json:"last_error,omitempty"`
}

type RestoreStore interface {
	PutRestore(context.Context, RestoreRecord) error
	GetRestore(context.Context, RestoreKey) (RestoreRecord, bool, error)
	ListRestores(context.Context, string) ([]RestoreRecord, error)
}

var restoreTransitions = map[RestoreState]map[RestoreState]struct{}{
	RestoreIntent:         {RestoreCreating: {}, RestoreReady: {}, RestoreManualReview: {}},
	RestoreCreating:       {RestoreReady: {}, RestoreManualReview: {}},
	RestoreReady:          {RestoreCleanupPending: {}, RestoreManualReview: {}},
	RestoreCleanupPending: {RestoreCleaned: {}, RestoreManualReview: {}},
}

func ValidateRestoreTransition(from, to RestoreState) error {
	if from == to {
		return nil
	}
	if _, ok := restoreTransitions[from][to]; !ok {
		return fmt.Errorf("invalid ClickHouse restore transition: %s -> %s", from, to)
	}
	return nil
}

func ValidateRestoreRecord(record RestoreRecord) error {
	if strings.TrimSpace(record.Key.RunID) == "" {
		return errors.New("ClickHouse restore run ID is required")
	}
	if !validIdentifier(record.Key.Database) || !validIdentifier(record.Key.Table) {
		return errors.New("ClickHouse restore database and table must be simple identifiers")
	}
	if record.Target.Database != record.Key.Database || record.Target.Table != record.Key.Table {
		return errors.New("ClickHouse restore target identity does not match its key")
	}
	if record.Target.UUID == "" || record.Target.Engine == "" || record.Target.SchemaFingerprint == "" {
		return errors.New("ClickHouse restore target UUID, engine and schema fingerprint are required")
	}
	if record.Restore.Database != record.Key.Database || !validIdentifier(record.Restore.Table) {
		return errors.New("ClickHouse restore object identity is invalid")
	}
	expected, err := BuildRestoreTableName(record.Key.RunID, record.Key.Table, record.OwnershipToken)
	if err != nil {
		return err
	}
	if record.Restore.Table != expected {
		return fmt.Errorf("ClickHouse restore object %q is not owned by run %q", record.Restore.Table, record.Key.RunID)
	}
	if record.Baseline.Rows > 0 && record.State != RestoreIntent && record.State != RestoreCreating && record.State != RestoreManualReview && record.Restore.UUID == "" {
		return errors.New("ready non-empty ClickHouse restore object UUID is required")
	}
	if record.State != RestoreIntent && record.State != RestoreCreating && record.State != RestoreManualReview && (record.Restore.Engine == "" || record.Restore.SchemaFingerprint == "") {
		return errors.New("ready ClickHouse restore object identity is incomplete")
	}
	if record.Baseline.Rows > 0 && (record.Baseline.HashSum64 == "" || record.Baseline.HashXor64 == "") {
		return errors.New("non-empty ClickHouse restore baseline requires dual hashes")
	}
	switch record.State {
	case RestoreIntent, RestoreCreating, RestoreReady, RestoreCleanupPending, RestoreCleaned, RestoreManualReview:
	default:
		return fmt.Errorf("unsupported ClickHouse restore state %q", record.State)
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("ClickHouse restore timestamps are invalid")
	}
	return nil
}
