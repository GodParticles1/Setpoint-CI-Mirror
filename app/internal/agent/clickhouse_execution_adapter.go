package agent

import (
	"context"
	"errors"
	"fmt"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type clickHouseOperationExecutionAdapter struct {
	client    clickhouse.QueryClient
	staging   clickhouse.StagingController
	transport clickhouse.NativeTransport
	verifier  clickhouse.FingerprintVerifier
	objects   clickhouse.RestoreObjectController
	authority ClickHouseOperationAuthority
}

func NewClickHouseOperationExecutionAdapter(
	client clickhouse.QueryClient,
	staging clickhouse.StagingController,
	transport clickhouse.NativeTransport,
	verifier clickhouse.FingerprintVerifier,
	objects clickhouse.RestoreObjectController,
	authority ClickHouseOperationAuthority,
) (OperationExecutionAdapter, error) {
	if client == nil || staging == nil || transport == nil || verifier == nil || objects == nil || authority == nil {
		return nil, errors.New("ClickHouse execution client, staging, transport, verifier, restore objects and authority are required")
	}
	return &clickHouseOperationExecutionAdapter{client: client, staging: staging, transport: transport, verifier: verifier, objects: objects, authority: authority}, nil
}

func (*clickHouseOperationExecutionAdapter) OperationID() string { return clickhouse.OperationID }

func (adapter *clickHouseOperationExecutionAdapter) Resolve(ctx context.Context, resource task.Resource) (ResolvedOperationExecution, error) {
	scope, err := operationActionScope(resource)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	ledger, err := newRemoteLedgerStore(ctx, adapter.authority, resource.Metadata.ID, scope)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	restores, err := newRemoteRestoreStore(ctx, adapter.authority, resource.Metadata.ID, scope)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	guard := &remoteClickHouseCommitGuard{authority: adapter.authority, taskID: resource.Metadata.ID, scope: scope, targets: append([]operation.Target(nil), resource.Spec.OperationExecution.Targets...)}
	commit, err := clickhouse.NewAtomicExchangeCommitEngine(ledger, adapter.client, adapter.verifier, guard)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	definition, err := clickhouse.NewDefinition(adapter.client, ledger, adapter.staging, adapter.transport, adapter.verifier)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	restore, err := clickhouse.NewExchangeRestoreProvider(ledger, restores, adapter.staging, adapter.objects, adapter.verifier, commit)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	return ResolvedOperationExecution{OperationID: clickhouse.OperationID, Definition: definition, RestoreProvider: restore}, nil
}

type remoteClickHouseCommitGuard struct {
	authority OperationLeaseAuthority
	taskID    string
	scope     protocol.OperationActionScope
	targets   []operation.Target
}

func (guard *remoteClickHouseCommitGuard) Verify(ctx context.Context, request clickhouse.CommitGuardRequest) error {
	lease, err := guard.authority.ValidateLease(ctx, guard.taskID, guard.scope)
	if err != nil {
		return fmt.Errorf("validate authoritative ClickHouse commit lease: %w", err)
	}
	resource := request.Database + "." + request.TargetTable
	for _, target := range guard.targets {
		if target.Kind != operation.TargetDataObject || target.Component != "clickhouse" || target.Resource != resource {
			continue
		}
		key, err := operation.ResourceLockKey(target)
		if err != nil {
			return err
		}
		local, err := clickhouse.NewLeaseCommitGuard(lease, key)
		if err != nil {
			return err
		}
		return local.Verify(ctx, request)
	}
	return fmt.Errorf("authoritative operation targets do not contain ClickHouse table %s", resource)
}

var _ clickhouse.CommitGuard = (*remoteClickHouseCommitGuard)(nil)
