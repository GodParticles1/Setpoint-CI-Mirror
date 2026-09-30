package task

import (
	"errors"

	"setpoint/internal/operation"
)

// ValidateOperationMutationEvidence is shared by Agent journaling and Server
// ingestion. It validates supplied evidence without synthesizing missing facts.
func ValidateOperationMutationEvidence(result OperationExecutionResult, phase Phase) error {
	if result.Apply != nil {
		if err := operation.ValidateMutationState(result.Apply.MutationState, result.Apply.Reconnect != nil); err != nil {
			return err
		}
		if result.Apply.Reconnect != nil {
			if result.Action != OperationActionApply || result.Rollback != nil || result.RestorePoint != nil || result.Verification != nil {
				return errors.New("apply reconnect requires only apply output")
			}
			if err := result.Apply.Reconnect.Validate(); err != nil {
				return err
			}
		}
	}
	if result.Rollback != nil {
		if err := operation.ValidateMutationState(result.Rollback.MutationState, phase == PhaseFailed || result.Rollback.Reconnect != nil); err != nil {
			return err
		}
		if phase == PhaseFailed && result.Rollback.Restored {
			return errors.New("failed rollback evidence cannot claim restored=true")
		}
		if result.Rollback.Reconnect != nil {
			if result.Action != OperationActionRollback || result.Apply != nil || result.RestorePoint != nil || result.Verification != nil {
				return errors.New("rollback reconnect requires only rollback output")
			}
			if err := result.Rollback.Reconnect.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}
