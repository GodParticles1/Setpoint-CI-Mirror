package agent

import (
	"context"
	"encoding/json"
	"errors"
	"setpoint/internal/operation"
	"setpoint/internal/task"
	"testing"
	"time"
)

type failedRollbackActionDefinition struct {
	*actionTestDefinition
	result operation.RollbackResult
	err    error
}

func (definition *failedRollbackActionDefinition) Rollback(context.Context, operation.RollbackInput) (operation.RollbackResult, error) {
	return definition.result, definition.err
}

func TestFailedRollbackPreservesMeaningfulTypedEvidence(t *testing.T) {
	base, metadata, restore := newActionTestRunner(t)
	definition := &failedRollbackActionDefinition{
		actionTestDefinition: &actionTestDefinition{metadata: metadata},
		result: operation.RollbackResult{
			Restored: false, MutationState: operation.MutationMayHaveChanged,
			Checkpoint: "rollback_partial",
			State:      operation.Artifact{SchemaVersion: "clickhouse.rollback.v1", Payload: json.RawMessage(`{"mutation_state":"MAY_HAVE_CHANGED"}`)},
		},
		err: errors.New("definition rollback failed after bounded recovery began"),
	}
	resource := actionTask(t, metadata, task.OperationActionRollback)
	now := time.Now().UTC()
	key, err := operation.ResourceLockKey(resource.Spec.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	authority := &authorityStub{lease: operation.LockLease{
		ID: "lease-rollback", OwnerID: "run-1", Resources: []operation.LockResource{{Key: key}},
		AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
	}}
	adapter, err := NewStaticOperationExecutionAdapter(metadata.ID, definition, restore)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewOperationExecutionResolver(adapter)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewOperationExecutionRunnerWithAuthority(base.registry, resolver, actionTestExecutor{}, "linux", authority)
	if err != nil {
		t.Fatal(err)
	}
	previousNow := runnerNow
	runnerNow = func() time.Time { return now }
	defer func() { runnerNow = previousNow }()

	result, runErr := runner.Execute(context.Background(), resource)
	if runErr == nil || result.Error == nil || result.Error.Code != "rollback_failed" || result.Rollback == nil {
		t.Fatalf("result=%#v err=%v", result, runErr)
	}
	if result.Rollback.MutationState != operation.MutationMayHaveChanged || result.Rollback.Checkpoint != "rollback_partial" {
		t.Fatalf("typed rollback evidence was not preserved: %#v", result.Rollback)
	}
}
