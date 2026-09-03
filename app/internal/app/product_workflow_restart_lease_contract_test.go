package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/operationrun"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

type canceledRestorePointRestartFixture struct {
	ctx        context.Context
	store      *storage.Store
	base       *OperationsService
	execution  *ProductExecutionResolver
	run        operationrun.Resource
	action     task.Resource
	supervisor *operation.LeaseSupervisor
}

func TestTerminalCanceledTaskRestartResumesAndReleasesPersistedLease(t *testing.T) {
	fixture := newCanceledRestorePointRestartFixture(t)
	runID := fixture.run.Metadata.ID
	contract := fixture.action.Spec.OperationExecution
	if contract == nil {
		t.Fatal("restore-point task lost its frozen contract")
	}
	if _, err := fixture.store.CompleteTask(fixture.ctx, fixture.action.Spec.NodeID, fixture.action.Metadata.ID, task.ResultSubmission{
		ClaimID: fixture.action.Status.ClaimID,
		Phase:   task.PhaseCanceled,
		OperationExecutionResult: &task.OperationExecutionResult{
			OperationID:        contract.OperationID,
			RunID:              contract.RunID,
			Action:             contract.Action,
			ParticipantNodeIDs: append([]string(nil), contract.ParticipantNodeIDs...),
			StageID:            contract.Stage.ID,
			StageIndex:         contract.StageIndex,
			ExecutorNodeID:     contract.Stage.ExecutorNodeID,
			Error:              &task.Failure{Code: "task_canceled", Message: "cancellation acknowledged"},
		},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	terminal, err := fixture.base.GetOperationRun(fixture.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status.State != operation.StateCanceledBeforeApply || (terminal.Execution != nil && terminal.Execution.Apply != nil) {
		t.Fatalf("terminal canceled run=%#v", terminal)
	}
	if _, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, runID); err != nil || !found {
		t.Fatalf("terminal lease before restart found=%v err=%v", found, err)
	}
	fixture.supervisor.Close()

	supervisor2, err := operation.NewLeaseSupervisor(fixture.store, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(supervisor2.Close)
	product2, err := NewProductOperations(fixture.base, fixture.store, supervisor2, fixture.execution)
	if err != nil {
		t.Fatal(err)
	}
	if err := product2.ResumeOperationRuns(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, runID); err != nil || found {
		t.Fatalf("terminal canceled lease found=%v err=%v", found, err)
	}
	competitor, err := fixture.store.Acquire(fixture.ctx, lockRequestForRun(t, fixture.run, "competing-run"))
	if err != nil {
		t.Fatalf("competitor did not acquire after terminal convergence: %v", err)
	}
	defer func() { _ = fixture.store.Release(context.Background(), competitor) }()
	if err := product2.ResumeOperationRuns(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	persistedCompetitor, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, "competing-run")
	if err != nil || !found || persistedCompetitor.ID != competitor.ID {
		t.Fatalf("duplicate restart changed competing lease=%#v found=%v err=%v", persistedCompetitor, found, err)
	}
	if _, err := fixture.store.GetTask(fixture.ctx, runID+":"+string(task.OperationActionApply)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("canceled restore-point task queued Apply: %v", err)
	}
}

func TestCanceledCreateRestorePointRestartReacquiresOnlyOnAuthoritativeAbsence(t *testing.T) {
	fixture := newCanceledRestorePointRestartFixture(t)
	runID := fixture.run.Metadata.ID
	previous, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, runID)
	if err != nil || !found {
		t.Fatalf("lease before explicit absence found=%v err=%v", found, err)
	}
	if err := fixture.supervisor.Release(fixture.ctx, runID); err != nil {
		t.Fatal(err)
	}
	fixture.supervisor.Close()

	supervisor2, err := operation.NewLeaseSupervisor(fixture.store, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(supervisor2.Close)
	product2, err := NewProductOperations(fixture.base, fixture.store, supervisor2, fixture.execution)
	if err != nil {
		t.Fatal(err)
	}
	if err := product2.ResumeOperationRuns(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	reacquired, found, err := supervisor2.CurrentLeaseByOwner(fixture.ctx, runID)
	if err != nil || !found || reacquired.ID == previous.ID {
		t.Fatalf("reacquired lease=%#v found=%v err=%v previous=%#v", reacquired, found, err, previous)
	}
	if _, err := fixture.store.Acquire(fixture.ctx, lockRequestForRun(t, fixture.run, "competing-run")); err == nil {
		t.Fatal("competing run acquired target after containment reacquire")
	}
}

func newCanceledRestorePointRestartFixture(t *testing.T) canceledRestorePointRestartFixture {
	t.Helper()
	ctx := context.Background()
	store, base := newOperationsServiceTest(t)
	run, created, err := base.CreateOperationRun(ctx, validOperationRunRequest())
	if err != nil || !created {
		t.Fatalf("create operation run created=%v err=%v", created, err)
	}
	planning, err := store.ClaimTask(ctx, "node-1", "planning-claim", time.Now().UTC())
	if err != nil || planning == nil {
		t.Fatalf("claim planning task=%#v err=%v", planning, err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-1", planning.Metadata.ID, "planning-claim", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	plan := operation.Plan{
		SchemaVersion: "test.v1",
		Summary:       "restart containment plan",
		Steps:         []operation.PlanStep{},
		Execution:     operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)},
	}
	impact := operation.Impact{Summary: "restart containment impact", Risk: operation.RiskHigh, Changes: []operation.Change{}}
	planningResult := operation.PlanningResult{
		OperationID: run.Spec.OperationID, OperationVersion: run.Spec.OperationVersion, CapabilityDigest: run.Spec.CapabilityDigest,
		State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC().Add(time.Second),
		Plan: &plan, Impact: &impact, PlanDigest: "sha256:restart-containment-plan",
	}
	if _, err := store.CompleteTask(ctx, "node-1", planning.Metadata.ID, task.ResultSubmission{
		ClaimID: "planning-claim", Phase: task.PhaseSucceeded, OperationResult: &planningResult,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	execution, err := NewProductExecutionResolver(ProductExecutionCapability{OperationID: clickhouse.OperationID, ApplyAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := operation.NewLeaseSupervisor(store, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(supervisor.Close)
	product, err := NewProductOperations(base, store, supervisor, execution)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := product.ConfirmOperationRun(ctx, run.Metadata.ID, protocol.ConfirmOperationRunRequest{
		IdempotencyKey: "confirm-restart-containment", PlanDigest: planningResult.PlanDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status.State != operation.StateCreatingRestorePoint {
		t.Fatalf("confirmed state=%s", confirmed.Status.State)
	}
	claimedAction, err := store.ClaimTask(ctx, "node-1", "restore-claim", time.Now().UTC())
	if err != nil || claimedAction == nil {
		t.Fatalf("claim restore task=%#v err=%v", claimedAction, err)
	}
	action, err := store.AcknowledgeTask(ctx, "node-1", claimedAction.Metadata.ID, "restore-claim", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := product.CancelOperationRun(ctx, run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	action, err = store.GetTask(ctx, action.Metadata.ID)
	if err != nil || action.Status.Phase != task.PhaseCancelRequested {
		t.Fatalf("canceled action=%#v err=%v", action, err)
	}
	return canceledRestorePointRestartFixture{ctx: ctx, store: store, base: base, execution: execution, run: canceled, action: action, supervisor: supervisor}
}

func lockRequestForRun(t *testing.T, run operationrun.Resource, ownerID string) operation.LockRequest {
	t.Helper()
	resources := make([]operation.LockResource, 0, len(operationExecutionTargets(run)))
	for _, target := range operationExecutionTargets(run) {
		key, err := operation.ResourceLockKey(target)
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, operation.LockResource{Key: key})
	}
	return operation.LockRequest{OwnerID: ownerID, Resources: resources, TTL: time.Minute}
}
