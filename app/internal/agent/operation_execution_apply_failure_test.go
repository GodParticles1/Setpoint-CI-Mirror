package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type failedApplyActionDefinition struct {
	*actionTestDefinition
	result operation.ApplyResult
	err    error
}

func (definition *failedApplyActionDefinition) Apply(context.Context, operation.ApplyInput) (operation.ApplyResult, error) {
	return definition.result, definition.err
}

type failedApplyDefinitionFactory struct {
	definition *failedApplyActionDefinition
}

func (factory failedApplyDefinitionFactory) Definition(clickhouse.LedgerStore) (operation.OperationDefinition, error) {
	return factory.definition, nil
}

func TestFailedApplyPreservesOnlyMeaningfulDefinitionEvidence(t *testing.T) {
	tests := []struct {
		name     string
		apply    operation.ApplyResult
		wantKeep bool
	}{
		{
			name: "meaningful",
			apply: operation.ApplyResult{
				Changed:    false,
				Checkpoint: "apply_partial",
				State: operation.Artifact{
					SchemaVersion: "clickhouse.apply.v1",
					Payload:       json.RawMessage(`{"run_id":"run-1","committed":[]}`),
				},
			},
			wantKeep: true,
		},
		{name: "zero", apply: operation.ApplyResult{}, wantKeep: false},
		{
			name:     "missing_checkpoint",
			apply:    operation.ApplyResult{State: operation.Artifact{SchemaVersion: "clickhouse.apply.v1", Payload: json.RawMessage(`{"committed":[]}`)}},
			wantKeep: false,
		},
		{
			name:     "invalid_json",
			apply:    operation.ApplyResult{Checkpoint: "apply_partial", State: operation.Artifact{SchemaVersion: "clickhouse.apply.v1", Payload: json.RawMessage(`{"committed":`)}},
			wantKeep: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, metadata, restore := newActionTestRunner(t)
			definition := &failedApplyActionDefinition{
				actionTestDefinition: &actionTestDefinition{metadata: metadata},
				result:               test.apply,
				err:                  errors.New("definition apply failed after bounded execution began"),
			}
			resource := actionTask(t, metadata, task.OperationActionApply)
			now := time.Now().UTC()
			key, err := operation.ResourceLockKey(resource.Spec.Targets[0])
			if err != nil {
				t.Fatal(err)
			}
			authority := &authorityStub{lease: operation.LockLease{
				ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: key}},
				AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
			}}
			runner, err := NewOperationExecutionRunnerWithAuthority(
				base.registry, restore, actionTestExecutor{}, "linux", authority,
				failedApplyDefinitionFactory{definition: definition},
			)
			if err != nil {
				t.Fatal(err)
			}
			previousNow := runnerNow
			runnerNow = func() time.Time { return now }
			defer func() { runnerNow = previousNow }()

			result, runErr := runner.Execute(context.Background(), resource)
			if runErr == nil || result.Error == nil || result.Error.Code != "apply_failed" {
				t.Fatalf("result=%#v err=%v", result, runErr)
			}
			if result.OperationID != resource.Spec.OperationExecution.OperationID || result.RunID != resource.Spec.OperationExecution.RunID || result.Action != task.OperationActionApply {
				t.Fatalf("result correlation=%#v", result)
			}
			if test.wantKeep {
				if result.Apply == nil || result.Apply.Checkpoint != test.apply.Checkpoint || string(result.Apply.State.Payload) != string(test.apply.State.Payload) {
					t.Fatalf("meaningful definition Apply evidence was not preserved: %#v", result.Apply)
				}
			} else if result.Apply != nil {
				t.Fatalf("zero or malformed Apply evidence must be dropped: %#v", result.Apply)
			}
		})
	}
}
