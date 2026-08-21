package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/operation"
)

type LeaseCommitGuard struct {
	lease       operation.LockLease
	resourceKey string
	now         func() time.Time
}

func NewLeaseCommitGuard(lease operation.LockLease, resourceKey string) (*LeaseCommitGuard, error) {
	resourceKey = strings.TrimSpace(resourceKey)
	if resourceKey == "" {
		return nil, errors.New("commit lock resource key is required")
	}
	guard := &LeaseCommitGuard{lease: lease, resourceKey: resourceKey, now: func() time.Time { return time.Now().UTC() }}
	if err := guard.validate(time.Now().UTC()); err != nil {
		return nil, err
	}
	return guard, nil
}

func (guard *LeaseCommitGuard) Verify(_ context.Context, request CommitGuardRequest) error {
	if request.RunID == "" {
		return errors.New("commit guard run ID is required")
	}
	if request.RunID != guard.lease.OwnerID {
		return fmt.Errorf("commit run %q does not own lock lease %q", request.RunID, guard.lease.ID)
	}
	if !validIdentifier(request.Database) || !validIdentifier(request.TargetTable) || !validIdentifier(request.StagingTable) {
		return errors.New("commit guard database, target table and staging table must be simple identifiers")
	}
	expectedStaging, err := BuildStagingTableName(request.RunID, request.TargetTable)
	if err != nil {
		return fmt.Errorf("derive run-owned staging name: %w", err)
	}
	if request.StagingTable != expectedStaging {
		return fmt.Errorf("staging table %q is not owned by run %q for target %s.%s; expected %q", request.StagingTable, request.RunID, request.Database, request.TargetTable, expectedStaging)
	}
	return guard.validate(guard.now())
}

func (guard *LeaseCommitGuard) validate(now time.Time) error {
	if err := operation.ValidateLease(guard.lease, now); err != nil {
		return fmt.Errorf("validate commit lock lease: %w", err)
	}
	for _, resource := range guard.lease.Resources {
		if resource.Key == guard.resourceKey {
			return nil
		}
	}
	return fmt.Errorf("lock lease %q does not cover resource %q", guard.lease.ID, guard.resourceKey)
}

func ClickHouseTableLockTarget(siteID, nodeID, database, table string) (operation.Target, error) {
	if !validIdentifier(database) || !validIdentifier(table) {
		return operation.Target{}, errors.New("ClickHouse lock database and table must be simple identifiers")
	}
	target := operation.Target{Kind: operation.TargetDataObject, SiteID: strings.TrimSpace(siteID), NodeID: strings.TrimSpace(nodeID), Component: "clickhouse", Resource: database + "." + table}
	if target.SiteID == "" && target.NodeID == "" {
		return operation.Target{}, errors.New("ClickHouse lock target requires site_id or node_id")
	}
	if err := operation.ValidateTarget(target); err != nil {
		return operation.Target{}, err
	}
	return target, nil
}
