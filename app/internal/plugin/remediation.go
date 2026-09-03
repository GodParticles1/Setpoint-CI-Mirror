package plugin

import (
	"errors"
	"fmt"
	"strings"
)

type RemediationDisposition string

const (
	RemediationAutoSafe      RemediationDisposition = "AUTO_SAFE"
	RemediationControlled    RemediationDisposition = "CONTROLLED"
	RemediationManualOnly    RemediationDisposition = "MANUAL_ONLY"
	RemediationNotApplicable RemediationDisposition = "NOT_APPLICABLE"
)

type RemediationMetadata struct {
	Disposition RemediationDisposition `json:"disposition"`
	OperationID string                 `json:"operation_id,omitempty"`
	Reason      string                 `json:"reason"`
}

func ValidateRemediationMetadata(metadata RemediationMetadata) error {
	if !ValidRemediationDisposition(metadata.Disposition) {
		return fmt.Errorf("invalid remediation disposition %q", metadata.Disposition)
	}
	if strings.TrimSpace(metadata.Reason) == "" {
		return errors.New("remediation reason is required")
	}
	operationID := strings.TrimSpace(metadata.OperationID)
	if metadata.Disposition == RemediationAutoSafe {
		if !validID(operationID) {
			return errors.New("AUTO_SAFE remediation requires a valid operation_id")
		}
	} else if operationID != "" {
		return errors.New("only AUTO_SAFE remediation may bind an operation_id")
	}
	return nil
}

func ValidRemediationDisposition(disposition RemediationDisposition) bool {
	switch disposition {
	case RemediationAutoSafe, RemediationControlled, RemediationManualOnly, RemediationNotApplicable:
		return true
	default:
		return false
	}
}

func (registry *CheckRegistry) SetRemediationMetadata(id string, metadata RemediationMetadata) error {
	id = strings.TrimSpace(id)
	if err := ValidateRemediationMetadata(metadata); err != nil {
		return fmt.Errorf("check %s remediation: %w", id, err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	current, exists := registry.checkItems[id]
	if !exists {
		return fmt.Errorf("unknown check %q", id)
	}
	if ValidRemediationDisposition(current.Remediation.Disposition) {
		return fmt.Errorf("check %s remediation disposition is already set", id)
	}
	metadata.OperationID = strings.TrimSpace(metadata.OperationID)
	metadata.Reason = strings.TrimSpace(metadata.Reason)
	current.Remediation = metadata
	registry.checkItems[id] = current
	return nil
}
