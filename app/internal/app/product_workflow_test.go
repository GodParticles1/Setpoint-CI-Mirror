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
	"setpoint/internal/task"
)

type productWorkflowRepo struct {
	run           operationrun.Resource
	tasks         map[string]task.Resource
	journal       []operation.JournalEntry
	continuations int
}

func (repo *productWorkflowRepo) CreateOperationRun(context.Context, operationrun.Resource, task.Resource) (operationrun.Resource, bool, error) {
	return operationrun.Resource{}, false, errors.New("unused")
}
func (repo *productWorkflowRepo) GetOperationRun(_ context.Context, id string) (operationrun.Resource, error) {
	if repo.run.Metadata.ID != id {
		return operationrun.Resource{}, errors.New("not found")
	}
	return repo.run, nil
}
func (repo *productWorkflowRepo) ListOperationRuns(context.Context, int, int) ([]operationrun.Resource, error) {
	return []operationrun.Resource{repo.run}, nil
}
func (repo *productWorkflowRepo) CancelOperationRun(_ context.Context, id string, at time.Time) (operationrun.Resource, error) {
	if repo.run.Metadata.ID != id {
		return operationrun.Resource{}, errors.New("not found")
	}
	current := repo.tasks[repo.run.Status.TaskID]
	if current.Spec.OperationExecution != nil && current.Spec.OperationExecution.StageIndex > 0 {
		repo.run.Status.Recovery = &operationrun.Recovery{Code: operationrun.RecoveryCancellationRequested, SafeNext: "rollback_applied_stages"}
		return repo.run, nil
	}
	repo.run.Status.State = operation.StateCanceledBeforeApply
	repo.run.Status.Checkpoint = "canceled_before_apply"
	repo.run.Status.UpdatedAt = at
	if !task.Terminal(current.Status.Phase) {
		if current.Status.Phase == task.PhasePending {
			current.Status.Phase = task.PhaseCanceled
		} else {
			current.Status.Phase = task.PhaseCancelRequested
		}
		repo.tasks[current.Metadata.ID] = current
	}
	return repo.run, nil
}
func (repo *productWorkflowRepo) GetTask(_ context.Context, id string) (task.Resource, error) {
	value, ok := repo.tasks[id]
	if !ok {
		return task.Resource{}, errors.New("not found")
	}
	return value, nil
}
func (repo *productWorkflowRepo) SaveOperationExecutionCheckpoint(_ context.Context, runID string, state operation.State, checkpoint string, snapshot operationrun.ExecutionSnapshot, recovery *operationrun.Recovery, journal operation.JournalEntry, at time.Time) (operationrun.Resource, error) {
	if repo.run.Metadata.ID != runID {
		return operationrun.Resource{}, errors.New("wrong run")
	}
	repo.run.Status.State = state
	repo.run.Status.Checkpoint = checkpoint
	repo.run.Status.UpdatedAt = at
	repo.run.Status.Recovery = recovery
	repo.journal = append(repo.journal, journal)
	return repo.run, nil
}
func (repo *productWorkflowRepo) ContinueOperationRun(_ context.Context, runID, completedTaskID string, state operation.State, checkpoint string, next task.Resource, journal operation.JournalEntry, at time.Time) (operationrun.Resource, error) {
	if repo.run.Metadata.ID != runID || repo.run.Status.TaskID != completedTaskID {
		return operationrun.Resource{}, task.ErrResultConflict
	}
	repo.continuations++
	repo.run.Status.State = state
	repo.run.Status.Checkpoint = checkpoint
	repo.run.Status.TaskID = next.Metadata.ID
	repo.run.Status.UpdatedAt = at
	repo.tasks[next.Metadata.ID] = next
	repo.journal = append(repo.journal, journal)
	return repo.run, nil
}
func (repo *productWorkflowRepo) List(_ context.Context, runID string) ([]operation.JournalEntry, error) {
	if repo.run.Metadata.ID != runID {
		return nil, errors.New("wrong run")
	}
	return append([]operation.JournalEntry(nil), repo.journal...), nil
}

type productWorkflowLease struct {
	lease      operation.LockLease
	acquires   int
	resumes    int
	releases   int
	held       bool
	acquireErr error
	resumeErr  error
	currentErr error
}

func (lease *productWorkflowLease) Acquire(context.Context, string, []operation.Target) (operation.LockLease, error) {
	lease.acquires++
	if lease.acquireErr != nil {
		return operation.LockLease{}, lease.acquireErr
	}
	lease.held = true
	return lease.lease, nil
}
func (lease *productWorkflowLease) Resume(context.Context, string, []operation.Target) (operation.LockLease, error) {
	lease.resumes++
	if lease.resumeErr != nil {
		return operation.LockLease{}, lease.resumeErr
	}
	if !lease.held {
		return operation.LockLease{}, operation.ErrLeaseAuthoritativeAbsence
	}
	return lease.lease, nil
}
func (lease *productWorkflowLease) CurrentLeaseByOwner(context.Context, string) (operation.LockLease, bool, error) {
	if lease.currentErr != nil {
		return operation.LockLease{}, false, lease.currentErr
	}
	if !lease.held {
		return operation.LockLease{}, false, operation.ErrLeaseAuthorityUnavailable
	}
	return lease.lease, true, nil
}
func (lease *productWorkflowLease) Release(context.Context, string) error {
	if !lease.held {
		return operation.ErrLeaseAuthorityUnavailable
	}
	lease.held = false
	lease.releases++
	return nil
}

func productWorkflowFixture(action task.OperationAction, phase task.Phase) (*ProductOperations, *productWorkflowRepo, *productWorkflowLease) {
	return productWorkflowFixtureForOperation(sysctlrepair.ID, action, phase)
}

func productWorkflowFixtureForOperation(operationID string, action task.OperationAction, phase task.Phase) (*ProductOperations, *productWorkflowRepo, *productWorkflowLease) {
	now := time.Date(2026, 8, 24, 4, 0, 0, 0, time.UTC)
	runID := "run-1"
	taskID := runID + ":" + string(action)
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	plan := operation.Plan{SchemaVersion: "setpoint.operation.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.plan.v1", Payload: []byte(`{"ok":true}`)}}
	restore := &operation.RestorePoint{ID: "rp-1"}
	apply := &operation.ApplyResult{Changed: true, Checkpoint: "applied", State: operation.Artifact{SchemaVersion: "test.state.v1", Payload: []byte(`{"value":0}`)}}
	rollback := &operation.RollbackResult{Restored: true, Checkpoint: "rolled_back", State: operation.Artifact{SchemaVersion: "test.state.v1", Payload: []byte(`{"value":1}`)}}
	state := map[task.OperationAction]operation.State{
		task.OperationActionCreateRestorePoint: operation.StateCreatingRestorePoint,
		task.OperationActionApply:              operation.StateRunning,
		task.OperationActionVerify:             operation.StateVerifying,
		task.OperationActionRollback:           operation.StateRollingBack,
		task.OperationActionVerifyRollback:     operation.StateRollingBack,
	}[action]
	run := operationrun.Resource{
		Metadata: operationrun.Metadata{ID: runID},
		Spec:     operationrun.Spec{OperationID: operationID, OperationVersion: "1.0.0", CapabilityDigest: "cap", NodeID: "node-1", Targets: targets, Parameters: []byte(`{"check_id":"net.ipv4.conf.all.accept_redirects.persisted","target_value":"runtime=0; persisted=0"}`)},
		Status:   operationrun.Status{State: state, Checkpoint: "action_" + string(action) + "_" + string(phase), TaskID: taskID, UpdatedAt: now},
		Plan:     &plan, PlanDigest: "plan-digest", Impact: &operation.Impact{Risk: operation.RiskLow},
		Execution: &operationrun.ExecutionSnapshot{RestorePoint: restore, Apply: apply, Rollback: rollback},
	}
	stages, err := operationrun.ExecutionStages(run)
	if err != nil || len(stages) == 0 {
		panic("single-node fixture has no execution stage")
	}
	stage := stages[0]
	contract := task.OperationExecutionContract{
		OperationID: operationID, RunID: runID, Action: action, PlanDigest: run.PlanDigest,
		ParticipantNodeIDs: []string{run.Spec.NodeID}, StageIndex: 0, Stage: stage,
		Targets: operationrun.StageTargets(run, stage), Plan: plan,
	}
	switch action {
	case task.OperationActionApply:
		contract.RestorePoint = restore
	case task.OperationActionVerify:
		contract.Apply = apply
	case task.OperationActionRollback:
		contract.RestorePoint = restore
		contract.Apply = apply
	case task.OperationActionVerifyRollback:
		contract.RestorePoint = restore
		contract.Rollback = rollback
	}
	frozen, digest, err := task.NewOperationExecutionContract(contract)
	if err != nil {
		panic(err)
	}
	current := task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindOperationExecutionTask, Metadata: task.Metadata{ID: taskID},
		Spec: task.Spec{
			NodeID: stage.ExecutorNodeID, OperationID: operationID, OperationVersion: run.Spec.OperationVersion,
			CapabilityDigest: run.Spec.CapabilityDigest, Targets: append([]operation.Target(nil), frozen.Targets...),
			Parameters: append([]byte(nil), run.Spec.Parameters...), SecretRefs: append([]operation.SecretRef(nil), run.Spec.SecretRefs...),
			OperationExecution: &frozen, ContractDigest: digest,
		},
		Status: task.Status{Phase: phase},
	}
	repo := &productWorkflowRepo{run: run, tasks: map[string]task.Resource{taskID: current}, journal: []operation.JournalEntry{{RunID: runID, Sequence: 1, State: state, Checkpoint: run.Status.Checkpoint, Message: "durable action result", At: now}}}
	lease := &productWorkflowLease{lease: operation.LockLease{ID: "lease-1", OwnerID: runID, Resources: []operation.LockResource{{Key: "node||node-1||"}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}, held: true}
	execution, err := NewProductExecutionResolver(ProductExecutionCapability{OperationID: operationID, ApplyAvailable: true})
	if err != nil {
		panic(err)
	}
	service := &ProductOperations{runs: repo, lease: lease, execution: execution, now: func() time.Time { return now.Add(time.Minute) }}
	return service, repo, lease
}

func TestClickHouseUsesSameGenericServerActionChain(t *testing.T) {
	service, repo, _ := productWorkflowFixtureForOperation(clickhouse.OperationID, task.OperationActionCreateRestorePoint, task.PhaseSucceeded)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	next := repo.tasks["run-1:apply"]
	if repo.continuations != 1 || next.Spec.OperationExecution == nil || next.Spec.OperationExecution.OperationID != clickhouse.OperationID || next.Spec.OperationExecution.Action != task.OperationActionApply {
		t.Fatalf("run=%#v next=%#v", repo.run.Status, next)
	}
}

func TestProductContinuationCreatesAtMostOneDeterministicNextTask(t *testing.T) {
	service, repo, _ := productWorkflowFixture(task.OperationActionCreateRestorePoint, task.PhaseSucceeded)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 || repo.run.Status.State != operation.StateRunning || repo.run.Status.TaskID != "run-1:apply" {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	next := repo.tasks["run-1:apply"]
	if next.Spec.OperationExecution == nil || next.Spec.OperationExecution.Action != task.OperationActionApply {
		t.Fatalf("next=%#v", next)
	}
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 {
		t.Fatalf("duplicate continuation count=%d", repo.continuations)
	}
}

func TestProductContinuationVerifyFailureQueuesRollbackOnlyWithDurablePrerequisites(t *testing.T) {
	service, repo, _ := productWorkflowFixture(task.OperationActionVerify, task.PhaseFailed)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 1 || repo.run.Status.State != operation.StateRollingBack || repo.run.Status.TaskID != "run-1:rollback" {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	next := repo.tasks["run-1:rollback"]
	if next.Spec.OperationExecution == nil || next.Spec.OperationExecution.Action != task.OperationActionRollback || next.Spec.OperationExecution.RestorePoint == nil || next.Spec.OperationExecution.Apply == nil {
		t.Fatalf("rollback task=%#v", next)
	}
}

func TestProductContinuationFailedApplyInterruptsWithoutRollback(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionApply, task.PhaseFailed)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.continuations != 0 || repo.run.Status.State != operation.StateInterrupted || repo.run.Status.Recovery == nil || !repo.run.Status.Recovery.ManualReview {
		t.Fatalf("run=%#v continuations=%d", repo.run.Status, repo.continuations)
	}
	if _, exists := repo.tasks["run-1:rollback"]; exists {
		t.Fatal("failed Apply must not blindly create rollback task")
	}
	if lease.releases != 0 {
		t.Fatal("interrupted mutation must keep supervised lease for reconciliation")
	}
}

func TestProductContinuationVerifySuccessTerminatesAndReleasesLease(t *testing.T) {
	service, repo, lease := productWorkflowFixture(task.OperationActionVerify, task.PhaseSucceeded)
	if err := service.ContinueOperationRun(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if repo.run.Status.State != operation.StateSucceeded || lease.releases != 1 {
		t.Fatalf("run=%#v releases=%d", repo.run.Status, lease.releases)
	}
}
