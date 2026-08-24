package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type OperationExecutionRunner interface {
	Execute(context.Context, task.Resource) (task.OperationExecutionResult, error)
}

type OperationExecutionDefinitionFactory interface {
	Definition(clickhouse.LedgerStore) (operation.OperationDefinition, error)
}

type operationExecutionRunner struct {
	registry  *operation.Registry
	restore   operation.RestorePointProvider
	executor  executor.CommandExecutor
	system    string
	authority OperationAuthority
	factory   OperationExecutionDefinitionFactory
}

func NewOperationExecutionRunner(registry *operation.Registry, restore operation.RestorePointProvider, commandExecutor executor.CommandExecutor, system string) (OperationExecutionRunner, error) {
	return newOperationExecutionRunner(registry, restore, commandExecutor, system, nil, nil)
}

func NewOperationExecutionRunnerWithAuthority(registry *operation.Registry, restore operation.RestorePointProvider, commandExecutor executor.CommandExecutor, system string, authority OperationAuthority, factory OperationExecutionDefinitionFactory) (OperationExecutionRunner, error) {
	if authority == nil || factory == nil {
		return nil, errors.New("server operation authority and execution definition factory are required")
	}
	return newOperationExecutionRunner(registry, restore, commandExecutor, system, authority, factory)
}

func newOperationExecutionRunner(registry *operation.Registry, restore operation.RestorePointProvider, commandExecutor executor.CommandExecutor, system string, authority OperationAuthority, factory OperationExecutionDefinitionFactory) (OperationExecutionRunner, error) {
	if registry == nil || restore == nil || commandExecutor == nil || system == "" {
		return nil, errors.New("operation registry, restore provider, executor and system are required")
	}
	return &operationExecutionRunner{registry: registry, restore: restore, executor: commandExecutor, system: system, authority: authority, factory: factory}, nil
}

func (runner *operationExecutionRunner) Execute(ctx context.Context, resource task.Resource) (task.OperationExecutionResult, error) {
	result := task.OperationExecutionResult{}
	if resource.Kind != task.KindOperationExecutionTask || resource.Spec.OperationExecution == nil {
		return result, errors.New("operation execution task contract is required")
	}
	if len(resource.Spec.SecretRefs) != 0 {
		return runner.fail(resource.Spec.OperationExecution, "secret_delivery_unavailable", errors.New("runtime SecretRef delivery is unavailable"))
	}
	contract := resource.Spec.OperationExecution
	result.OperationID, result.RunID, result.Action = contract.OperationID, contract.RunID, contract.Action
	if err := task.ValidateOperationExecutionContract(*contract, resource.Spec.ContractDigest); err != nil {
		return runner.fail(contract, "operation_contract_invalid", err)
	}
	if contract.OperationID != resource.Spec.OperationID {
		return runner.fail(contract, "operation_correlation_mismatch", errors.New("operation ID differs from task specification"))
	}
	if !reflect.DeepEqual(contract.Targets, resource.Spec.Targets) {
		return runner.fail(contract, "operation_target_mismatch", errors.New("bounded action targets differ from task specification"))
	}
	definition, ok := runner.registry.Definition(resource.Spec.OperationID)
	if !ok {
		return runner.fail(contract, "operation_capability_unavailable", fmt.Errorf("operation %q has no execution definition", resource.Spec.OperationID))
	}
	metadata := definition.Metadata()
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil {
		return runner.fail(contract, "operation_capability_invalid", err)
	}
	if metadata.ID != contract.OperationID || metadata.Version != resource.Spec.OperationVersion || digest != resource.Spec.CapabilityDigest {
		return runner.fail(contract, "operation_capability_mismatch", errors.New("registered operation does not match the frozen bounded-action contract"))
	}

	runtime := operation.RuntimeInput{Executor: runner.executor, Parameters: resource.Spec.Parameters, System: runner.system, Targets: append([]operation.Target(nil), resource.Spec.Targets...)}

	switch contract.Action {
	case task.OperationActionCreateRestorePoint:
		point, err := runner.restore.Create(ctx, operation.RestorePointRequest{OperationID: contract.OperationID, RunID: contract.RunID, Targets: append([]operation.Target(nil), contract.Targets...), Plan: contract.Plan})
		if err != nil {
			return runner.fail(contract, "create_restore_point_failed", err)
		}
		verification, err := runner.restore.Verify(ctx, point)
		if err != nil {
			return runner.fail(contract, "verify_restore_point_failed", err)
		}
		if !verification.Passed {
			return runner.fail(contract, "verify_restore_point_failed", errors.New("restore point verification did not pass"))
		}
		result.RestorePoint = &point
		return result, nil
	case task.OperationActionVerify:
		verification, err := definition.Verify(ctx, operation.VerifyInput{Runtime: runtime, Plan: contract.Plan, Apply: *contract.Apply})
		if err != nil {
			return runner.fail(contract, "verify_failed", err)
		}
		result.Verification = &verification
		if !verification.Passed {
			return runner.failWithResult(result, "verify_failed", errors.New("operation verification did not pass"))
		}
		return result, nil
	case task.OperationActionVerifyRollback:
		verification, err := definition.VerifyRollback(ctx, operation.VerifyRollbackInput{Runtime: runtime, Plan: contract.Plan, Rollback: *contract.Rollback, RestorePoint: *contract.RestorePoint})
		if err != nil {
			return runner.fail(contract, "verify_rollback_failed", err)
		}
		result.Verification = &verification
		if !verification.Passed {
			return runner.failWithResult(result, "verify_rollback_failed", errors.New("rollback verification did not pass"))
		}
		return result, nil
	case task.OperationActionApply, task.OperationActionRollback:
		return runner.executeDestructive(ctx, resource, runtime, result)
	default:
		return runner.fail(contract, "operation_action_unsupported", fmt.Errorf("unsupported operation action %q", contract.Action))
	}
}

func (runner *operationExecutionRunner) executeDestructive(ctx context.Context, resource task.Resource, runtime operation.RuntimeInput, result task.OperationExecutionResult) (task.OperationExecutionResult, error) {
	contract := resource.Spec.OperationExecution
	if runner.authority == nil || runner.factory == nil {
		return runner.fail(contract, "operation_authority_unavailable", errors.New("destructive bounded action requires server-authoritative lease and ledger adapters"))
	}
	scope, err := operationActionScope(resource)
	if err != nil {
		return runner.fail(contract, "operation_authority_invalid", err)
	}
	lease, err := newRemoteLeaseHandle(ctx, runner.authority, resource.Metadata.ID, scope)
	if err != nil {
		return runner.fail(contract, "operation_lease_authority_unavailable", err)
	}
	if err := lease.Validate(runnerNow()); err != nil {
		return runner.fail(contract, "operation_lease_authority_rejected", err)
	}
	ledger, err := newRemoteLedgerStore(ctx, runner.authority, resource.Metadata.ID, scope)
	if err != nil {
		return runner.fail(contract, "operation_ledger_authority_unavailable", err)
	}
	definition, err := runner.factory.Definition(ledger)
	if err != nil {
		return runner.fail(contract, "operation_ledger_authority_unavailable", err)
	}
	metadata := definition.Metadata()
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil || metadata.ID != resource.Spec.OperationID || metadata.Version != resource.Spec.OperationVersion || digest != resource.Spec.CapabilityDigest {
		if err == nil {
			err = errors.New("authoritative execution definition does not match the frozen bounded-action contract")
		}
		return runner.fail(contract, "operation_capability_mismatch", err)
	}

	switch contract.Action {
	case task.OperationActionApply:
		if contract.Impact == nil || contract.RestorePoint == nil {
			return runner.fail(contract, "operation_contract_invalid", errors.New("apply action requires frozen impact and restore point"))
		}
		apply, err := definition.Apply(ctx, operation.ApplyInput{Runtime: runtime, Plan: contract.Plan, Impact: *contract.Impact, RestorePoint: *contract.RestorePoint, Lease: lease})
		if err != nil {
			if meaningfulApplyFailureEvidence(apply) {
				result.Apply = &apply
				return runner.failWithResult(result, "apply_failed", err)
			}
			return runner.fail(contract, "apply_failed", err)
		}
		result.Apply = &apply
		return result, nil
	case task.OperationActionRollback:
		if contract.Apply == nil || contract.RestorePoint == nil {
			return runner.fail(contract, "operation_contract_invalid", errors.New("rollback action requires frozen apply result and restore point"))
		}
		rollback, err := definition.Rollback(ctx, operation.RollbackInput{Runtime: runtime, Plan: contract.Plan, Apply: *contract.Apply, RestorePoint: *contract.RestorePoint, Lease: lease})
		if err != nil {
			return runner.fail(contract, "rollback_failed", err)
		}
		result.Rollback = &rollback
		return result, nil
	default:
		return runner.fail(contract, "operation_action_unsupported", fmt.Errorf("unsupported destructive operation action %q", contract.Action))
	}
}

func meaningfulApplyFailureEvidence(result operation.ApplyResult) bool {
	return strings.TrimSpace(result.Checkpoint) != "" &&
		strings.TrimSpace(result.State.SchemaVersion) != "" &&
		len(result.State.Payload) != 0 && json.Valid(result.State.Payload)
}

var runnerNow = func() time.Time { return time.Now().UTC() }

func (runner *operationExecutionRunner) fail(contract *task.OperationExecutionContract, code string, err error) (task.OperationExecutionResult, error) {
	result := task.OperationExecutionResult{}
	if contract != nil {
		result.OperationID, result.RunID, result.Action = contract.OperationID, contract.RunID, contract.Action
	}
	return runner.failWithResult(result, code, err)
}

func (runner *operationExecutionRunner) failWithResult(result task.OperationExecutionResult, code string, err error) (task.OperationExecutionResult, error) {
	result.Error = &task.Failure{Code: code, Message: err.Error()}
	return result, err
}

var _ OperationExecutionRunner = (*operationExecutionRunner)(nil)
