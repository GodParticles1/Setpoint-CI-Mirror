package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

var ErrOperationAuthorityUnavailable = errors.New("server operation authority is unavailable")

type authoritativeOperationLedger interface {
	clickhouse.LedgerStore
}

type authoritativeOperationRestoreStore interface {
	clickhouse.RestoreStore
}

func (service *Service) ValidateOperationLease(ctx context.Context, agentID, taskID string, request protocol.OperationLeaseValidationRequest) (protocol.OperationLeaseValidationResponse, error) {
	_, contract, err := service.authorizedOperationAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return protocol.OperationLeaseValidationResponse{}, err
	}
	if contract.Action != task.OperationActionApply && contract.Action != task.OperationActionRollback {
		return protocol.OperationLeaseValidationResponse{}, &ValidationError{Err: errors.New("lease validation is only available for destructive operation actions")}
	}
	if service.leaseAuthority == nil {
		return protocol.OperationLeaseValidationResponse{}, ErrOperationAuthorityUnavailable
	}
	lease, found, err := service.leaseAuthority.CurrentLeaseByOwner(ctx, contract.RunID)
	if err != nil {
		if errors.Is(err, operation.ErrLeaseAuthorityUnavailable) {
			return protocol.OperationLeaseValidationResponse{}, ErrOperationAuthorityUnavailable
		}
		return protocol.OperationLeaseValidationResponse{}, fmt.Errorf("read authoritative operation lease: %w", err)
	}
	if !found {
		return protocol.OperationLeaseValidationResponse{}, &ConflictError{Err: errors.New("no active authoritative operation lease")}
	}
	resources := make([]operation.LockResource, 0, len(contract.Targets))
	for _, target := range contract.Targets {
		key, err := operation.ResourceLockKey(target)
		if err != nil {
			return protocol.OperationLeaseValidationResponse{}, &ValidationError{Err: fmt.Errorf("operation action target: %w", err)}
		}
		resources = append(resources, operation.LockResource{Key: key})
	}
	if err := operation.ValidateLeaseCoverage(lease, contract.RunID, resources, service.now().UTC()); err != nil {
		return protocol.OperationLeaseValidationResponse{}, &ConflictError{Err: fmt.Errorf("authoritative operation lease rejected: %w", err)}
	}
	return protocol.OperationLeaseValidationResponse{Lease: lease}, nil
}

func (service *Service) PutOperationLedger(ctx context.Context, agentID, taskID string, request protocol.OperationLedgerPutRequest) error {
	_, contract, err := service.authorizedOperationLedgerAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return err
	}
	if request.Entry.Key.RunID != contract.RunID {
		return &ValidationError{Err: errors.New("ledger entry run ID differs from the authorized operation run")}
	}
	ledger, ok := service.nodes.(authoritativeOperationLedger)
	if !ok {
		return ErrOperationAuthorityUnavailable
	}
	if err := ledger.Put(ctx, request.Entry); err != nil {
		return fmt.Errorf("persist authoritative ClickHouse migration ledger: %w", err)
	}
	return nil
}

func (service *Service) GetOperationLedger(ctx context.Context, agentID, taskID string, request protocol.OperationLedgerGetRequest) (protocol.OperationLedgerGetResponse, error) {
	_, contract, err := service.authorizedOperationLedgerAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return protocol.OperationLedgerGetResponse{}, err
	}
	if request.Key.RunID != contract.RunID {
		return protocol.OperationLedgerGetResponse{}, &ValidationError{Err: errors.New("ledger key run ID differs from the authorized operation run")}
	}
	ledger, ok := service.nodes.(authoritativeOperationLedger)
	if !ok {
		return protocol.OperationLedgerGetResponse{}, ErrOperationAuthorityUnavailable
	}
	entry, found, err := ledger.Get(ctx, request.Key)
	if err != nil {
		return protocol.OperationLedgerGetResponse{}, fmt.Errorf("read authoritative ClickHouse migration ledger: %w", err)
	}
	return protocol.OperationLedgerGetResponse{Entry: entry, Found: found}, nil
}

func (service *Service) ListOperationLedger(ctx context.Context, agentID, taskID string, request protocol.OperationLedgerListRunRequest) (protocol.OperationLedgerListRunResponse, error) {
	_, contract, err := service.authorizedOperationLedgerAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return protocol.OperationLedgerListRunResponse{}, err
	}
	ledger, ok := service.nodes.(authoritativeOperationLedger)
	if !ok {
		return protocol.OperationLedgerListRunResponse{}, ErrOperationAuthorityUnavailable
	}
	entries, err := ledger.ListRun(ctx, contract.RunID)
	if err != nil {
		return protocol.OperationLedgerListRunResponse{}, fmt.Errorf("list authoritative ClickHouse migration ledger: %w", err)
	}
	return protocol.OperationLedgerListRunResponse{Entries: entries}, nil
}

func (service *Service) PutOperationRestore(ctx context.Context, agentID, taskID string, request protocol.OperationRestorePutRequest) error {
	_, contract, err := service.authorizedClickHouseAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return err
	}
	if request.Record.Key.RunID != contract.RunID {
		return &ValidationError{Err: errors.New("restore record run ID differs from the authorized operation run")}
	}
	store, ok := service.nodes.(authoritativeOperationRestoreStore)
	if !ok {
		return ErrOperationAuthorityUnavailable
	}
	if err := store.PutRestore(ctx, request.Record); err != nil {
		return fmt.Errorf("persist authoritative ClickHouse restore record: %w", err)
	}
	return nil
}

func (service *Service) GetOperationRestore(ctx context.Context, agentID, taskID string, request protocol.OperationRestoreGetRequest) (protocol.OperationRestoreGetResponse, error) {
	_, contract, err := service.authorizedClickHouseAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return protocol.OperationRestoreGetResponse{}, err
	}
	if request.Key.RunID != contract.RunID {
		return protocol.OperationRestoreGetResponse{}, &ValidationError{Err: errors.New("restore key run ID differs from the authorized operation run")}
	}
	store, ok := service.nodes.(authoritativeOperationRestoreStore)
	if !ok {
		return protocol.OperationRestoreGetResponse{}, ErrOperationAuthorityUnavailable
	}
	record, found, err := store.GetRestore(ctx, request.Key)
	if err != nil {
		return protocol.OperationRestoreGetResponse{}, fmt.Errorf("read authoritative ClickHouse restore record: %w", err)
	}
	return protocol.OperationRestoreGetResponse{Record: record, Found: found}, nil
}

func (service *Service) ListOperationRestores(ctx context.Context, agentID, taskID string, request protocol.OperationRestoreListRunRequest) (protocol.OperationRestoreListRunResponse, error) {
	_, contract, err := service.authorizedClickHouseAction(ctx, agentID, taskID, request.Scope)
	if err != nil {
		return protocol.OperationRestoreListRunResponse{}, err
	}
	store, ok := service.nodes.(authoritativeOperationRestoreStore)
	if !ok {
		return protocol.OperationRestoreListRunResponse{}, ErrOperationAuthorityUnavailable
	}
	records, err := store.ListRestores(ctx, contract.RunID)
	if err != nil {
		return protocol.OperationRestoreListRunResponse{}, fmt.Errorf("list authoritative ClickHouse restore records: %w", err)
	}
	return protocol.OperationRestoreListRunResponse{Records: records}, nil
}

func (service *Service) authorizedOperationLedgerAction(ctx context.Context, agentID, taskID string, scope protocol.OperationActionScope) (task.Resource, *task.OperationExecutionContract, error) {
	return service.authorizedClickHouseAction(ctx, agentID, taskID, scope)
}

func (service *Service) authorizedClickHouseAction(ctx context.Context, agentID, taskID string, scope protocol.OperationActionScope) (task.Resource, *task.OperationExecutionContract, error) {
	resource, contract, err := service.authorizedOperationAction(ctx, agentID, taskID, scope)
	if err != nil {
		return task.Resource{}, nil, err
	}
	if contract.OperationID != clickhouse.OperationID {
		return task.Resource{}, nil, &ValidationError{Err: errors.New("ClickHouse authority is unavailable for a different operation capability")}
	}
	return resource, contract, nil
}

func (service *Service) authorizedOperationAction(ctx context.Context, agentID, taskID string, scope protocol.OperationActionScope) (task.Resource, *task.OperationExecutionContract, error) {
	agentID = strings.TrimSpace(agentID)
	taskID = strings.TrimSpace(taskID)
	scope.ClaimID = strings.TrimSpace(scope.ClaimID)
	scope.RunID = strings.TrimSpace(scope.RunID)
	if err := validateIdentifier(agentID); err != nil {
		return task.Resource{}, nil, &ValidationError{Err: fmt.Errorf("agent_id: %w", err)}
	}
	if err := validateIdentifier(taskID); err != nil {
		return task.Resource{}, nil, &ValidationError{Err: fmt.Errorf("task_id: %w", err)}
	}
	if err := validateIdentifier(scope.ClaimID); err != nil {
		return task.Resource{}, nil, &ValidationError{Err: fmt.Errorf("scope.claim_id: %w", err)}
	}
	if err := validateIdentifier(scope.RunID); err != nil {
		return task.Resource{}, nil, &ValidationError{Err: fmt.Errorf("scope.run_id: %w", err)}
	}
	if !task.ValidOperationAction(scope.Action) {
		return task.Resource{}, nil, &ValidationError{Err: fmt.Errorf("unsupported operation action %q", scope.Action)}
	}
	resource, err := service.nodes.GetTask(ctx, taskID)
	if err != nil {
		return task.Resource{}, nil, err
	}
	if resource.Kind != task.KindOperationExecutionTask || resource.Spec.OperationExecution == nil {
		return task.Resource{}, nil, &ValidationError{Err: errors.New("operation authority requires an OperationExecutionTask")}
	}
	if resource.Spec.NodeID != agentID {
		return task.Resource{}, nil, &ConflictError{Err: task.ErrNodeMismatch}
	}
	if resource.Status.ClaimID == "" || resource.Status.ClaimID != scope.ClaimID {
		return task.Resource{}, nil, &ConflictError{Err: task.ErrClaimMismatch}
	}
	if resource.Status.Phase != task.PhaseRunning {
		return task.Resource{}, nil, &ConflictError{Err: fmt.Errorf("%w: operation authority requires running task", task.ErrInvalidTransition)}
	}
	if len(resource.Spec.SecretRefs) != 0 {
		return task.Resource{}, nil, &ConflictError{Err: ErrSecretDeliveryUnavailable}
	}
	contract := resource.Spec.OperationExecution
	if err := task.ValidateOperationExecutionContract(*contract, resource.Spec.ContractDigest); err != nil {
		return task.Resource{}, nil, &ValidationError{Err: fmt.Errorf("operation execution contract: %w", err)}
	}
	if contract.RunID != scope.RunID || contract.Action != scope.Action {
		return task.Resource{}, nil, &ConflictError{Err: errors.New("operation action scope does not match the frozen task contract")}
	}
	return resource, contract, nil
}
