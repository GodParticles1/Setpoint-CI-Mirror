package agent

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type countingActionDefinition struct {
	*actionTestDefinition
	applyCalls    int
	rollbackCalls int
}

func (definition *countingActionDefinition) Apply(context.Context, operation.ApplyInput) (operation.ApplyResult, error) {
	definition.applyCalls++
	return operation.ApplyResult{}, nil
}

func (definition *countingActionDefinition) Rollback(context.Context, operation.RollbackInput) (operation.RollbackResult, error) {
	definition.rollbackCalls++
	return operation.RollbackResult{Restored: true}, nil
}

type countingDefinitionFactory struct{ definition *countingActionDefinition }

func (factory countingDefinitionFactory) Definition(clickhouse.LedgerStore) (operation.OperationDefinition, error) {
	return factory.definition, nil
}

func TestDestructiveBoundedActionsInvokeExactlyOneDefinitionAction(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		t.Run(string(action), func(t *testing.T) {
			base, metadata, restore := newActionTestRunner(t)
			definition := &countingActionDefinition{actionTestDefinition: &actionTestDefinition{metadata: metadata}}
			resource := actionTask(t, metadata, action)
			now := time.Now().UTC()
			key, err := operation.ResourceLockKey(resource.Spec.Targets[0])
			if err != nil { t.Fatal(err) }
			authority := &authorityStub{lease: operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: key}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}}
			runner, err := NewOperationExecutionRunnerWithAuthority(base.registry, restore, actionTestExecutor{}, "linux", authority, countingDefinitionFactory{definition: definition})
			if err != nil { t.Fatal(err) }
			previousNow := runnerNow
			runnerNow = func() time.Time { return now }
			defer func() { runnerNow = previousNow }()
			result, err := runner.Execute(context.Background(), resource)
			if err != nil { t.Fatalf("result=%#v err=%v", result, err) }
			if action == task.OperationActionApply && (definition.applyCalls != 1 || definition.rollbackCalls != 0) { t.Fatalf("apply=%d rollback=%d", definition.applyCalls, definition.rollbackCalls) }
			if action == task.OperationActionRollback && (definition.rollbackCalls != 1 || definition.applyCalls != 0) { t.Fatalf("apply=%d rollback=%d", definition.applyCalls, definition.rollbackCalls) }
		})
	}
}
