package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"setpoint/internal/operation"
)

const (
	KindOperationExecutionTask        = "OperationExecutionTask"
	OperationExecutionContractVersion = 2
)

type OperationAction string

const (
	OperationActionCreateRestorePoint OperationAction = "create_restore_point"
	OperationActionApply              OperationAction = "apply"
	OperationActionVerify             OperationAction = "verify"
	OperationActionRollback           OperationAction = "rollback"
	OperationActionVerifyRollback     OperationAction = "verify_rollback"
)

func ValidOperationAction(action OperationAction) bool {
	switch action {
	case OperationActionCreateRestorePoint, OperationActionApply, OperationActionVerify, OperationActionRollback, OperationActionVerifyRollback:
		return true
	default:
		return false
	}
}

type OperationExecutionContract struct {
	SchemaVersion int                       `json:"schema_version"`
	OperationID   string                    `json:"operation_id"`
	RunID         string                    `json:"run_id"`
	Action        OperationAction           `json:"action"`
	PlanDigest    string                    `json:"plan_digest"`
	Targets       []operation.Target        `json:"targets"`
	Plan          operation.Plan            `json:"plan"`
	Impact        *operation.Impact         `json:"impact,omitempty"`
	RestorePoint  *operation.RestorePoint   `json:"restore_point,omitempty"`
	Apply         *operation.ApplyResult    `json:"apply,omitempty"`
	Rollback      *operation.RollbackResult `json:"rollback,omitempty"`
}

type OperationExecutionResult struct {
	OperationID  string                    `json:"operation_id"`
	RunID        string                    `json:"run_id"`
	Action       OperationAction           `json:"action"`
	RestorePoint *operation.RestorePoint   `json:"restore_point,omitempty"`
	Apply        *operation.ApplyResult    `json:"apply,omitempty"`
	Verification *operation.Verification   `json:"verification,omitempty"`
	Rollback     *operation.RollbackResult `json:"rollback,omitempty"`
	Error        *Failure                  `json:"error,omitempty"`
}

func NewOperationExecutionContract(contract OperationExecutionContract) (OperationExecutionContract, string, error) {
	contract.OperationID = strings.TrimSpace(contract.OperationID)
	contract.RunID = strings.TrimSpace(contract.RunID)
	contract.PlanDigest = strings.TrimSpace(contract.PlanDigest)
	contract.Targets = append([]operation.Target(nil), contract.Targets...)
	if contract.SchemaVersion == 0 {
		contract.SchemaVersion = OperationExecutionContractVersion
	}
	if err := validateOperationExecutionContract(contract); err != nil {
		return OperationExecutionContract{}, "", err
	}
	digest, err := operationExecutionContractDigest(contract)
	if err != nil {
		return OperationExecutionContract{}, "", err
	}
	return contract, digest, nil
}

func ValidateOperationExecutionContract(contract OperationExecutionContract, digest string) error {
	if err := validateOperationExecutionContract(contract); err != nil {
		return err
	}
	actual, err := operationExecutionContractDigest(contract)
	if err != nil {
		return err
	}
	if strings.TrimSpace(digest) == "" || !strings.EqualFold(actual, strings.TrimSpace(digest)) {
		return errors.New("operation execution contract digest does not match its frozen content")
	}
	return nil
}

func CloneOperationExecutionContract(contract *OperationExecutionContract) *OperationExecutionContract {
	if contract == nil {
		return nil
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return nil
	}
	var copy OperationExecutionContract
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return nil
	}
	return &copy
}

func validateOperationExecutionContract(contract OperationExecutionContract) error {
	if contract.SchemaVersion != OperationExecutionContractVersion {
		return fmt.Errorf("unsupported operation execution contract schema version %d", contract.SchemaVersion)
	}
	if !validContractIdentifier(contract.OperationID) || !validContractIdentifier(contract.RunID) {
		return errors.New("operation execution contract requires valid operation and run IDs")
	}
	if !ValidOperationAction(contract.Action) {
		return fmt.Errorf("unsupported operation action %q", contract.Action)
	}
	if strings.TrimSpace(contract.PlanDigest) == "" {
		return errors.New("operation execution contract requires the confirmed plan digest")
	}
	if len(contract.Targets) == 0 {
		return errors.New("operation execution contract requires at least one target")
	}
	for _, target := range contract.Targets {
		if err := operation.ValidateTarget(target); err != nil {
			return fmt.Errorf("validate operation execution target: %w", err)
		}
	}
	if strings.TrimSpace(contract.Plan.SchemaVersion) == "" || strings.TrimSpace(contract.Plan.Execution.SchemaVersion) == "" || len(contract.Plan.Execution.Payload) == 0 || !json.Valid(contract.Plan.Execution.Payload) {
		return errors.New("operation execution contract requires a versioned executable plan")
	}

	switch contract.Action {
	case OperationActionCreateRestorePoint:
		if contract.RestorePoint != nil || contract.Apply != nil || contract.Rollback != nil {
			return errors.New("create_restore_point must not carry execution outputs")
		}
	case OperationActionApply:
		if contract.RestorePoint == nil {
			return errors.New("apply requires a verified restore point")
		}
	case OperationActionVerify:
		if contract.Apply == nil {
			return errors.New("verify requires the persisted apply result")
		}
	case OperationActionRollback:
		if contract.RestorePoint == nil {
			return errors.New("rollback requires the persisted restore point")
		}
	case OperationActionVerifyRollback:
		if contract.RestorePoint == nil || contract.Rollback == nil {
			return errors.New("verify_rollback requires persisted restore point and rollback result")
		}
	}
	return nil
}

func operationExecutionContractDigest(contract OperationExecutionContract) (string, error) {
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("encode operation execution contract: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func OperationExecutionContractDigest(contract OperationExecutionContract) (string, error) {
	return operationExecutionContractDigest(contract)
}
