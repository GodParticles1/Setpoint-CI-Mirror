package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/operation/sysctlrepair"
	"setpoint/internal/operationrun"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

func TestProductCapabilityResolverReportsExecutableAvailabilityAndFailsUnknownClosed(t *testing.T) {
	resolver, err := NewProductExecutionResolver(
		ProductExecutionCapability{OperationID: sysctlrepair.ID, ApplyAvailable: true},
		ProductExecutionCapability{OperationID: clickhouse.OperationID, ApplyAvailable: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(sysctlrepair.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(clickhouse.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	service := &ProductOperations{base: &OperationsService{catalog: registry}, execution: resolver}
	listed, err := service.ListOperations()
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	for _, operationID := range []string{sysctlrepair.ID, clickhouse.OperationID} {
		resource, err := service.GetOperation(operationID)
		if err != nil {
			t.Fatal(err)
		}
		if !resource.Availability.Apply || resource.Availability.BlockCode != "" {
			t.Fatalf("operation=%s availability=%#v", operationID, resource.Availability)
		}
	}
	unknown := service.productOperationAvailability(operationrun.DefinitionResource{Metadata: operation.Metadata{ID: "operation.unknown"}, Availability: operationrun.Availability{Planning: true}})
	if unknown.Availability.Apply || unknown.Availability.BlockCode != OperationExecutionUnavailableBlock {
		t.Fatalf("unknown availability=%#v", unknown.Availability)
	}
}

func TestProductConfirmQueuesExactlyOneRestoreActionForBothCapabilities(t *testing.T) {
	for _, operationID := range []string{sysctlrepair.ID, clickhouse.OperationID} {
		t.Run(operationID, func(t *testing.T) {
			service, repo, lease := productWorkflowFixtureForOperation(operationID, task.OperationActionCreateRestorePoint, task.PhasePending)
			if operationID == clickhouse.OperationID {
				dataTarget := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}
				repo.run.Plan.Steps = []operation.PlanStep{{ID: "commit", Target: dataTarget, Writes: true}}
				dataKey, err := operation.ResourceLockKey(dataTarget)
				if err != nil {
					t.Fatal(err)
				}
				lease.lease.Resources = append(lease.lease.Resources, operation.LockResource{Key: dataKey})
			}
			repo.run.Status = operationrun.Status{State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", TaskID: "planning-task", UpdatedAt: repo.run.Status.UpdatedAt}
			repo.tasks = map[string]task.Resource{"planning-task": {Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: "planning-task"}, Status: task.Status{Phase: task.PhaseSucceeded}}}
			service.base = &OperationsService{runs: repo}
			lease.lease.Resources = []operation.LockResource{{Key: "node||node-1||"}}
			confirmed, err := service.ConfirmOperationRun(context.Background(), "run-1", protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-1", PlanDigest: "plan-digest"})
			if err != nil {
				t.Fatal(err)
			}
			if confirmed.Status.State != operation.StateCreatingRestorePoint || !confirmed.Status.ApplyAvailable || repo.continuations != 1 || repo.run.Status.TaskID != "run-1:create_restore_point" {
				t.Fatalf("confirmed=%#v continuations=%d", confirmed.Status, repo.continuations)
			}
			created := repo.tasks["run-1:create_restore_point"]
			if created.Spec.OperationExecution == nil || len(created.Spec.OperationExecution.Targets) != len(operationExecutionTargets(repo.run)) {
				t.Fatalf("create restore task targets=%#v", created.Spec.OperationExecution)
			}
			if _, err := service.ConfirmOperationRun(context.Background(), "run-1", protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-1", PlanDigest: "plan-digest"}); err != nil {
				t.Fatal(err)
			}
			if repo.continuations != 1 {
				t.Fatalf("duplicate Confirm queued %d continuations", repo.continuations)
			}
		})
	}
}

func TestProductSuccessfulActionChainReachesVerifiedTerminalState(t *testing.T) {
	for _, operationID := range []string{sysctlrepair.ID, clickhouse.OperationID} {
		t.Run(operationID, func(t *testing.T) {
			service, repo, lease := productWorkflowFixtureForOperation(operationID, task.OperationActionCreateRestorePoint, task.PhaseSucceeded)
			if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
				t.Fatal(err)
			}
			apply := repo.tasks["run-1:apply"]
			apply.Status.Phase = task.PhaseSucceeded
			repo.tasks[apply.Metadata.ID] = apply
			repo.run.Status.Checkpoint = "action_apply_succeeded"
			if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
				t.Fatal(err)
			}
			verify := repo.tasks["run-1:verify"]
			verify.Status.Phase = task.PhaseSucceeded
			repo.tasks[verify.Metadata.ID] = verify
			repo.run.Status.Checkpoint = "action_verify_succeeded"
			if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
				t.Fatal(err)
			}
			if repo.run.Status.State != operation.StateSucceeded || lease.releases != 1 || repo.continuations != 2 {
				t.Fatalf("run=%#v releases=%d continuations=%d", repo.run.Status, lease.releases, repo.continuations)
			}
		})
	}
}

func TestProductConfirmFailsClosedForUnknownCapabilityAndUnavailableSecrets(t *testing.T) {
	service, repo, _ := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhasePending)
	repo.run.Status.State = operation.StateAwaitingConfirm
	service.base = &OperationsService{runs: repo}
	repo.run.Spec.OperationID = "operation.unknown"
	if _, err := service.ConfirmOperationRun(context.Background(), "run-1", protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-1", PlanDigest: "plan-digest"}); !errors.Is(err, ErrOperationExecutionUnavailable) {
		t.Fatalf("unknown capability error=%v", err)
	}
	repo.run.Spec.OperationID = sysctlrepair.ID
	repo.run.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "future", Reference: "ref-1"}}
	if _, err := service.ConfirmOperationRun(context.Background(), "run-1", protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-1", PlanDigest: "plan-digest"}); !errors.Is(err, ErrSecretDeliveryUnavailable) {
		t.Fatalf("secret boundary error=%v", err)
	}
}

func TestProductRestartResumesExactCheckpointWithoutDuplicateTask(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhasePending)
	repo.run.Status = operationrun.Status{State: operation.StateAcquiringLock, Checkpoint: "lease_acquired", TaskID: "planning-task", UpdatedAt: time.Now().UTC()}
	repo.tasks = map[string]task.Resource{"planning-task": {Kind: task.KindOperationPlanningTask, Metadata: task.Metadata{ID: "planning-task"}, Status: task.Status{Phase: task.PhaseSucceeded}}}
	lease.lease.Resources = []operation.LockResource{{Key: "node||node-1||"}}
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 || repo.run.Status.TaskID != "run-1:create_restore_point" {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	if err := service.ResumeOperationRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 {
		t.Fatalf("restart retry duplicated action task: %d", repo.continuations)
	}
}
