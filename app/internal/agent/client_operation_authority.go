package agent

import (
	"context"
	"errors"
	"net/url"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/protocol"
)

var ErrOperationAuthorityConfiguration = errors.New("operation authority client and agent identity are required")

type clientOperationAuthority struct {
	client  *Client
	agentID string
}

func NewClientOperationAuthority(client *Client, agentID string) (ClickHouseOperationAuthority, error) {
	if client == nil || agentID == "" {
		return nil, ErrOperationAuthorityConfiguration
	}
	return &clientOperationAuthority{client: client, agentID: agentID}, nil
}

func (authority *clientOperationAuthority) ValidateLease(ctx context.Context, taskID string, scope protocol.OperationActionScope) (operation.LockLease, error) {
	var response protocol.OperationLeaseValidationResponse
	err := authority.client.post(ctx, authority.path(taskID, "lease/validate"), protocol.OperationLeaseValidationRequest{Scope: scope}, &response, authority.client.Credential())
	return response.Lease, err
}

func (authority *clientOperationAuthority) PutLedger(ctx context.Context, taskID string, scope protocol.OperationActionScope, entry clickhouse.LedgerEntry) error {
	var response map[string]string
	return authority.client.post(ctx, authority.path(taskID, "ledger/put"), protocol.OperationLedgerPutRequest{Scope: scope, Entry: entry}, &response, authority.client.Credential())
}

func (authority *clientOperationAuthority) GetLedger(ctx context.Context, taskID string, scope protocol.OperationActionScope, key clickhouse.LedgerKey) (clickhouse.LedgerEntry, bool, error) {
	var response protocol.OperationLedgerGetResponse
	err := authority.client.post(ctx, authority.path(taskID, "ledger/get"), protocol.OperationLedgerGetRequest{Scope: scope, Key: key}, &response, authority.client.Credential())
	return response.Entry, response.Found, err
}

func (authority *clientOperationAuthority) ListLedger(ctx context.Context, taskID string, scope protocol.OperationActionScope) ([]clickhouse.LedgerEntry, error) {
	var response protocol.OperationLedgerListRunResponse
	err := authority.client.post(ctx, authority.path(taskID, "ledger/list-run"), protocol.OperationLedgerListRunRequest{Scope: scope}, &response, authority.client.Credential())
	return response.Entries, err
}

func (authority *clientOperationAuthority) PutRestore(ctx context.Context, taskID string, scope protocol.OperationActionScope, record clickhouse.RestoreRecord) error {
	var response map[string]string
	return authority.client.post(ctx, authority.path(taskID, "restore/put"), protocol.OperationRestorePutRequest{Scope: scope, Record: record}, &response, authority.client.Credential())
}

func (authority *clientOperationAuthority) GetRestore(ctx context.Context, taskID string, scope protocol.OperationActionScope, key clickhouse.RestoreKey) (clickhouse.RestoreRecord, bool, error) {
	var response protocol.OperationRestoreGetResponse
	err := authority.client.post(ctx, authority.path(taskID, "restore/get"), protocol.OperationRestoreGetRequest{Scope: scope, Key: key}, &response, authority.client.Credential())
	return response.Record, response.Found, err
}

func (authority *clientOperationAuthority) ListRestores(ctx context.Context, taskID string, scope protocol.OperationActionScope) ([]clickhouse.RestoreRecord, error) {
	var response protocol.OperationRestoreListRunResponse
	err := authority.client.post(ctx, authority.path(taskID, "restore/list-run"), protocol.OperationRestoreListRunRequest{Scope: scope}, &response, authority.client.Credential())
	return response.Records, err
}

func (authority *clientOperationAuthority) path(taskID, suffix string) string {
	return "/api/v1/agents/" + url.PathEscape(authority.agentID) + "/tasks/" + url.PathEscape(taskID) + "/operation-authority/" + suffix
}

var _ ClickHouseOperationAuthority = (*clientOperationAuthority)(nil)
