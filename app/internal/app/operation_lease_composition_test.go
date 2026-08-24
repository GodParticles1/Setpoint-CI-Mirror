package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type leaseCompositionRepository struct {
	NodeRepository
	resource    task.Resource
	directLease operation.LockLease
	directReads int
}

func (repository *leaseCompositionRepository) GetTask(context.Context, string) (task.Resource, error) {
	return task.Clone(repository.resource), nil
}

func (repository *leaseCompositionRepository) CurrentLeaseByOwner(context.Context, string) (operation.LockLease, bool, error) {
	repository.directReads++
	return repository.directLease, true, nil
}

type leaseCompositionAuthority struct {
	lease operation.LockLease
	found bool
	err   error
	calls int
}

func (authority *leaseCompositionAuthority) CurrentLeaseByOwner(context.Context, string) (operation.LockLease, bool, error) {
	authority.calls++
	return authority.lease, authority.found, authority.err
}

func TestValidateOperationLeaseDoesNotFallbackToNodeRepository(t *testing.T) {
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	resource, request, lease := leaseCompositionFixture(t, now)
	repository := &leaseCompositionRepository{resource: resource, directLease: lease}
	service := &Service{nodes: repository, now: func() time.Time { return now }}

	if _, err := service.ValidateOperationLease(context.Background(), "agent-1", "task-1", request); !errors.Is(err, ErrOperationAuthorityUnavailable) {
		t.Fatalf("expected unavailable authority, got %v", err)
	}
	if repository.directReads != 0 {
		t.Fatalf("direct node lease authority was read %d times", repository.directReads)
	}
}

func TestValidateOperationLeaseUsesBoundAuthorityOnly(t *testing.T) {
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	resource, request, lease := leaseCompositionFixture(t, now)
	repository := &leaseCompositionRepository{resource: resource, directLease: lease}
	authority := &leaseCompositionAuthority{lease: lease, found: true}
	service := &Service{nodes: repository, leaseAuthority: authority, now: func() time.Time { return now }}

	response, err := service.ValidateOperationLease(context.Background(), "agent-1", "task-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Lease.ID != lease.ID || authority.calls != 1 || repository.directReads != 0 {
		t.Fatalf("response=%#v authority_calls=%d direct_reads=%d", response, authority.calls, repository.directReads)
	}
}

func TestValidateOperationLeaseSupervisorFailureDoesNotFallback(t *testing.T) {
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	resource, request, lease := leaseCompositionFixture(t, now)
	repository := &leaseCompositionRepository{resource: resource, directLease: lease}
	authority := &leaseCompositionAuthority{err: operation.ErrLeaseAuthorityUnavailable}
	service := &Service{nodes: repository, leaseAuthority: authority, now: func() time.Time { return now }}

	if _, err := service.ValidateOperationLease(context.Background(), "agent-1", "task-1", request); !errors.Is(err, ErrOperationAuthorityUnavailable) {
		t.Fatalf("expected fail-closed operation authority, got %v", err)
	}
	if authority.calls != 1 || repository.directReads != 0 {
		t.Fatalf("authority_calls=%d direct_reads=%d", authority.calls, repository.directReads)
	}
}

func TestNodeRepositoryBoundaryRemainsNarrow(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"operation.LockManager", "clickhouse.LedgerStore"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("NodeRepository boundary widened with %q", forbidden)
		}
	}
}

func leaseCompositionFixture(t *testing.T, now time.Time) (task.Resource, protocol.OperationLeaseValidationRequest, operation.LockLease) {
	t.Helper()
	target := operation.Target{Kind: operation.TargetNode, NodeID: "agent-1"}
	contract, digest, err := task.NewOperationExecutionContract(task.OperationExecutionContract{
		OperationID: "operation.test",
		RunID:       "run-1",
		Action:      task.OperationActionApply,
		PlanDigest:  "sha256:plan",
		Targets:     []operation.Target{target},
		Plan: operation.Plan{
			SchemaVersion: "test.plan.v1",
			Execution:     operation.Artifact{SchemaVersion: "test.exec.v1", Payload: json.RawMessage(`{}`)},
		},
		RestorePoint: &operation.RestorePoint{},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource := task.Resource{
		APIVersion: "setpoint.io/v1",
		Kind:       task.KindOperationExecutionTask,
		Metadata:   task.Metadata{ID: "task-1"},
		Spec: task.Spec{
			NodeID:             "agent-1",
			OperationExecution: &contract,
			ContractDigest:     digest,
		},
		Status: task.Status{Phase: task.PhaseRunning, ClaimID: "claim-1"},
	}
	key, err := operation.ResourceLockKey(target)
	if err != nil {
		t.Fatal(err)
	}
	lease := operation.LockLease{
		ID:         "lease-1",
		OwnerID:    "run-1",
		Resources:  []operation.LockResource{{Key: key}},
		AcquiredAt: now.Add(-time.Minute),
		ExpiresAt:  now.Add(time.Minute),
	}
	request := protocol.OperationLeaseValidationRequest{Scope: protocol.OperationActionScope{
		ClaimID: "claim-1",
		RunID:   "run-1",
		Action:  task.OperationActionApply,
	}}
	return resource, request, lease
}
