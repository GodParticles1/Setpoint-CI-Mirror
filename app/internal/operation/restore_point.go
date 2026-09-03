package operation

import (
	"context"
	"errors"
	"strings"
	"time"
)

type RestorePointStatus string

const (
	RestorePointCreated  RestorePointStatus = "created"
	RestorePointVerified RestorePointStatus = "verified"
	RestorePointRestored RestorePointStatus = "restored"
	RestorePointInvalid  RestorePointStatus = "invalid"
)

type RestorePoint struct {
	ID          string             `json:"id"`
	ProviderID  string             `json:"provider_id"`
	OperationID string             `json:"operation_id"`
	RunID       string             `json:"run_id"`
	Status      RestorePointStatus `json:"status"`
	Targets     []Target           `json:"targets"`
	CreatedAt   time.Time          `json:"created_at"`
	ExpiresAt   *time.Time         `json:"expires_at,omitempty"`
	Manifest    Artifact           `json:"manifest"`
	Evidence    []EvidenceRef      `json:"evidence,omitempty"`
}

type RestorePointRequest struct {
	OperationID string
	RunID       string
	Targets     []Target
	Plan        Plan
	Stage       *PlanStep
	Retention   time.Duration
}

type RestorePointProvider interface {
	ID() string
	Create(context.Context, RestorePointRequest) (RestorePoint, error)
	Verify(context.Context, RestorePoint) (Verification, error)
	Restore(context.Context, RestorePoint, ApplyResult) (RollbackResult, error)
	VerifyRestored(context.Context, RestorePoint, RollbackResult) (Verification, error)
}

func ValidateRestorePoint(point RestorePoint, now time.Time) error {
	if strings.TrimSpace(point.ID) == "" || strings.TrimSpace(point.ProviderID) == "" || strings.TrimSpace(point.OperationID) == "" || strings.TrimSpace(point.RunID) == "" {
		return errors.New("restore point identity is incomplete")
	}
	if point.CreatedAt.IsZero() {
		return errors.New("restore point creation time is required")
	}
	if point.Status != RestorePointVerified && point.Status != RestorePointRestored {
		return errors.New("restore point is not verified")
	}
	if point.ExpiresAt != nil && !now.Before(*point.ExpiresAt) {
		return errors.New("restore point is expired")
	}
	if len(point.Targets) == 0 {
		return errors.New("restore point requires at least one target")
	}
	for _, target := range point.Targets {
		if err := ValidateTarget(target); err != nil {
			return err
		}
	}
	if strings.TrimSpace(point.Manifest.SchemaVersion) == "" || len(point.Manifest.Payload) == 0 {
		return errors.New("restore point manifest is required")
	}
	return nil
}
