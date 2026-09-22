package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/api"
	"setpoint/internal/app"
	"setpoint/internal/domain"
	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/xrocketreaddress"
	"setpoint/internal/operationrun"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

func TestXRocketRuntimeUserflowSucceedsWithoutPhysicalMutation(t *testing.T) {
	fixture := newXRocketRuntimeFixture(t, false)
	run := fixture.createAndPlan(t, "xrocket-runtime-success")

	catalog := runtimeManagementRequest(t, fixture.management, http.MethodGet, "/api/v1/operations/"+xrocketreaddress.OperationID, nil)
	if catalog.Code != http.StatusOK || !bytes.Contains(catalog.Body.Bytes(), []byte(`"apply":true`)) {
		t.Fatalf("xRocket catalog capability status=%d body=%s", catalog.Code, catalog.Body.String())
	}

	fixture.confirmAndProveIdempotent(t, run)
	fixture.processExecutionActions(t, 3)

	stored := fixture.getRun(t, run.Metadata.ID)
	if stored.Status.State != operation.StateSucceeded || stored.Status.Checkpoint != "verified" {
		t.Fatalf("final run state=%s checkpoint=%s recovery=%#v", stored.Status.State, stored.Status.Checkpoint, stored.Status.Recovery)
	}
	if fixture.definition.applyCalls != 1 || fixture.definition.verifyCalls != 1 || fixture.definition.rollbackCalls != 0 || fixture.definition.verifyRollbackCalls != 0 {
		t.Fatalf("action calls apply=%d verify=%d rollback=%d verifyRollback=%d",
			fixture.definition.applyCalls, fixture.definition.verifyCalls, fixture.definition.rollbackCalls, fixture.definition.verifyRollbackCalls)
	}
	fixture.assertNoLease(t, run.Metadata.ID)
	fixture.assertNoPhysicalMutation(t)
}

func TestXRocketRuntimeUserflowVerifyFailureRollsBackWithoutPhysicalMutation(t *testing.T) {
	fixture := newXRocketRuntimeFixture(t, true)
	run := fixture.createAndPlan(t, "xrocket-runtime-rollback")
	fixture.confirmAndProveIdempotent(t, run)
	fixture.processExecutionActions(t, 5)

	stored := fixture.getRun(t, run.Metadata.ID)
	if stored.Status.State != operation.StateRolledBack || stored.Status.Checkpoint != "rollback_verified" {
		t.Fatalf("final run state=%s checkpoint=%s recovery=%#v", stored.Status.State, stored.Status.Checkpoint, stored.Status.Recovery)
	}
	if fixture.definition.applyCalls != 1 || fixture.definition.verifyCalls != 1 || fixture.definition.rollbackCalls != 1 || fixture.definition.verifyRollbackCalls != 1 {
		t.Fatalf("action calls apply=%d verify=%d rollback=%d verifyRollback=%d",
			fixture.definition.applyCalls, fixture.definition.verifyCalls, fixture.definition.rollbackCalls, fixture.definition.verifyRollbackCalls)
	}
	fixture.assertNoLease(t, run.Metadata.ID)
	fixture.assertNoPhysicalMutation(t)
}

type xrocketRuntimeFixture struct {
	ctx          context.Context
	store        *storage.Store
	baseService  *app.Service
	product      *app.ProductOperations
	productSvc   *app.ProductService
	management   http.Handler
	operations   *operation.Registry
	checks       *plugin.CheckRegistry
	executor     *runtimeNoMutationExecutor
	definition   *runtimeXRocketDefinition
	resolver     *agent.OperationExecutionResolver
	authority    *runtimeLeaseAuthority
	journalPath  string
}

func newXRocketRuntimeFixture(t *testing.T, failVerify bool) *xrocketRuntimeFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, filepath.Join(dir, "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if _, err := store.RegisterNode(ctx, domain.Registration{
		AgentID: "node-1", Hostname: "node-1", OS: "linux", OSVersion: "integration", Arch: "amd64", AgentVersion: "integration", ReceivedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	checks := plugin.NewCheckRegistry()
	leaseSupervisor, err := operation.NewLeaseSupervisor(store, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leaseSupervisor.Close)

	baseService, err := app.NewServiceWithOperationLeaseAuthority(store, store, checks, time.Minute, leaseSupervisor)
	if err != nil {
		t.Fatal(err)
	}
	definition := &runtimeXRocketDefinition{failVerify: failVerify}
	operations := operation.NewRegistry()
	if err := operations.Register(definition); err != nil {
		t.Fatal(err)
	}
	baseOperations, err := app.NewOperationsService(store, store, operations, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	productResolver, err := app.NewProductExecutionResolver(app.ProductExecutionCapability{
		OperationID: xrocketreaddress.OperationID, ApplyAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	product, err := app.NewProductOperations(baseOperations, store, leaseSupervisor, productResolver)
	if err != nil {
		t.Fatal(err)
	}
	productSvc, err := app.NewProductService(baseService, product)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	management, err := api.NewManagementHandlerWithOperations(store, productSvc, product, logger)
	if err != nil {
		t.Fatal(err)
	}

	restore := &runtimeRestoreProvider{}
	adapter, err := agent.NewStaticOperationExecutionAdapter(xrocketreaddress.OperationID, definition, restore)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := agent.NewOperationExecutionResolver(adapter)
	if err != nil {
		t.Fatal(err)
	}
	return &xrocketRuntimeFixture{
		ctx: ctx, store: store, baseService: baseService, product: product, productSvc: productSvc,
		management: management, operations: operations, checks: checks, executor: &runtimeNoMutationExecutor{},
		definition: definition, resolver: resolver,
		authority: &runtimeLeaseAuthority{service: baseService, agentID: "node-1"},
		journalPath: filepath.Join(dir, "execution-journal.json"),
	}
}

func (fixture *xrocketRuntimeFixture) createAndPlan(t *testing.T, key string) operationrun.Resource {
	t.Helper()
	request := protocol.CreateOperationRunRequest{APIVersion: "setpoint.io/v1", Kind: "OperationRun"}
	request.Metadata.IdempotencyKey = key
	request.Spec.OperationID = xrocketreaddress.OperationID
	request.Spec.NodeID = "node-1"
	request.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	request.Spec.Parameters = json.RawMessage(`{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","external_db_target_address":"203.0.113.20"}`)
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	created := runtimeManagementRequest(t, fixture.management, http.MethodPost, "/api/v1/operation-runs", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var run operationrun.Resource
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}

	remote := &runtimeTaskRemote{service: fixture.productSvc}
	journal, err := agent.NewTaskJournal(filepath.Join(filepath.Dir(fixture.journalPath), "planning-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := agent.NewTaskWorkerWithOperations(remote, "node-1", "linux", fixture.checks, fixture.operations, fixture.executor, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	run = fixture.getRun(t, run.Metadata.ID)
	if run.Status.State != operation.StateAwaitingConfirm || run.PlanDigest == "" || !run.Status.ApplyAvailable {
		t.Fatalf("planned run state=%s digest=%q apply=%v", run.Status.State, run.PlanDigest, run.Status.ApplyAvailable)
	}
	return run
}

func (fixture *xrocketRuntimeFixture) confirmAndProveIdempotent(t *testing.T, run operationrun.Resource) {
	t.Helper()
	payload, _ := json.Marshal(protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-" + run.Metadata.ID, PlanDigest: run.PlanDigest})
	confirmed := runtimeManagementRequest(t, fixture.management, http.MethodPost, "/api/v1/operation-runs/"+run.Metadata.ID+"/confirm", payload)
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	before := fixture.executionTaskCount(t)

	duplicate := runtimeManagementRequest(t, fixture.management, http.MethodPost, "/api/v1/operation-runs/"+run.Metadata.ID+"/confirm", payload)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate confirm status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if got := fixture.executionTaskCount(t); got != before {
		t.Fatalf("duplicate confirm created execution task: before=%d after=%d", before, got)
	}

	if err := fixture.product.ResumeOperationRuns(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if err := fixture.product.ResumeOperationRuns(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if got := fixture.executionTaskCount(t); got != before {
		t.Fatalf("server resume duplicated next action: before=%d after=%d", before, got)
	}
}

func (fixture *xrocketRuntimeFixture) processExecutionActions(t *testing.T, count int) {
	t.Helper()
	remote := &runtimeTaskRemote{service: fixture.productSvc}
	for index := 0; index < count; index++ {
		journal, err := agent.NewTaskJournal(fixture.journalPath)
		if err != nil {
			t.Fatal(err)
		}
		runner, err := agent.NewOperationExecutionRunnerWithAuthority(fixture.operations, fixture.resolver, fixture.executor, "linux", fixture.authority)
		if err != nil {
			t.Fatal(err)
		}
		worker, err := agent.NewTaskWorkerWithControlledOperations(remote, "node-1", "linux", fixture.checks, fixture.operations, runner, fixture.executor, journal, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := worker.ProcessOne(fixture.ctx); err != nil {
			t.Fatalf("action %d: %v", index, err)
		}
		// Rebuild ProductOperations' durable continuation view after each action.
		// Resume must be idempotent and must never fan out a duplicate task.
		before := fixture.executionTaskCount(t)
		if err := fixture.product.ResumeOperationRuns(fixture.ctx); err != nil {
			t.Fatalf("resume after action %d: %v", index, err)
		}
		if got := fixture.executionTaskCount(t); got != before {
			t.Fatalf("resume after action %d duplicated task: before=%d after=%d", index, before, got)
		}
	}
}

func (fixture *xrocketRuntimeFixture) getRun(t *testing.T, runID string) operationrun.Resource {
	t.Helper()
	response := runtimeManagementRequest(t, fixture.management, http.MethodGet, "/api/v1/operation-runs/"+runID, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get run status=%d body=%s", response.Code, response.Body.String())
	}
	var run operationrun.Resource
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	return run
}

func (fixture *xrocketRuntimeFixture) executionTaskCount(t *testing.T) int {
	t.Helper()
	resources, err := fixture.store.ListTasks(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, resource := range resources {
		if resource.Kind == task.KindOperationExecutionTask {
			count++
		}
	}
	return count
}

func (fixture *xrocketRuntimeFixture) assertNoLease(t *testing.T, runID string) {
	t.Helper()
	if lease, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, runID); err != nil || found {
		t.Fatalf("terminal run retained authoritative lease: found=%v lease=%#v err=%v", found, lease, err)
	}
}

func (fixture *xrocketRuntimeFixture) assertNoPhysicalMutation(t *testing.T) {
	t.Helper()
	if fixture.executor.calls != 0 {
		t.Fatalf("runtime userflow executed %d host commands", fixture.executor.calls)
	}
}

type runtimeTaskRemote struct{ service *app.ProductService }

func (remote *runtimeTaskRemote) ClaimTask(ctx context.Context, agentID string) (*task.Resource, error) {
	return remote.service.ClaimTask(ctx, agentID)
}
func (remote *runtimeTaskRemote) AcknowledgeTask(ctx context.Context, agentID, taskID, claimID string) (task.Resource, error) {
	return remote.service.AcknowledgeTask(ctx, agentID, taskID, protocol.AcknowledgeTaskRequest{ClaimID: claimID})
}
func (remote *runtimeTaskRemote) SubmitTaskResult(ctx context.Context, agentID, taskID string, submission task.ResultSubmission) (task.Resource, error) {
	return remote.service.SubmitTaskResult(ctx, agentID, taskID, submission)
}

type runtimeLeaseAuthority struct {
	service *app.Service
	agentID string
}

func (authority *runtimeLeaseAuthority) ValidateLease(ctx context.Context, taskID string, scope protocol.OperationActionScope) (operation.LockLease, error) {
	response, err := authority.service.ValidateOperationLease(ctx, authority.agentID, taskID, protocol.OperationLeaseValidationRequest{Scope: scope})
	return response.Lease, err
}

type runtimeNoMutationExecutor struct{ calls int }

func (runner *runtimeNoMutationExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	runner.calls++
	return executor.Result{}, errors.New("runtime userflow must not execute host commands")
}

type runtimeXRocketDefinition struct {
	failVerify          bool
	applyCalls          int
	verifyCalls         int
	rollbackCalls       int
	verifyRollbackCalls int
}

func (definition *runtimeXRocketDefinition) Metadata() operation.Metadata {
	return xrocketreaddress.Metadata()
}

func (definition *runtimeXRocketDefinition) Discover(_ context.Context, input operation.DiscoverInput) (operation.Discovery, error) {
	return operation.Discovery{
		Applicable: true, Summary: "safe runtime discovery", Targets: append([]operation.Target(nil), input.Runtime.Targets...),
		Snapshot: operation.Artifact{SchemaVersion: "integration.xrocket.discovery.v1", Payload: json.RawMessage(`{"safe":true}`)},
	}, nil
}

func (definition *runtimeXRocketDefinition) Precheck(context.Context, operation.PrecheckInput) (operation.Precheck, error) {
	return operation.Precheck{
		Passed: true, Summary: "safe runtime precheck",
		Snapshot: operation.Artifact{SchemaVersion: "integration.xrocket.precheck.v1", Payload: json.RawMessage(`{"safe":true}`)},
	}, nil
}

func (definition *runtimeXRocketDefinition) Plan(context.Context, operation.PlanInput) (operation.Plan, error) {
	return operation.Plan{
		SchemaVersion: "integration.xrocket.plan.v1", Summary: "safe runtime plan",
		Execution: operation.Artifact{SchemaVersion: "integration.xrocket.plan.v1", Payload: json.RawMessage(`{"safe":true}`)},
	}, nil
}

func (definition *runtimeXRocketDefinition) Impact(context.Context, operation.ImpactInput) (operation.Impact, error) {
	return operation.Impact{Summary: "safe runtime impact", Risk: operation.RiskCritical}, nil
}

func (definition *runtimeXRocketDefinition) Apply(_ context.Context, input operation.ApplyInput) (operation.ApplyResult, error) {
	definition.applyCalls++
	if err := input.Lease.Validate(time.Now().UTC()); err != nil {
		return operation.ApplyResult{}, err
	}
	return operation.ApplyResult{
		Changed: true, MutationState: operation.MutationChanged, Checkpoint: "safe_apply",
		State: operation.Artifact{SchemaVersion: "integration.xrocket.apply.v1", Payload: json.RawMessage(`{"safe":true}`)},
	}, nil
}

func (definition *runtimeXRocketDefinition) Verify(context.Context, operation.VerifyInput) (operation.Verification, error) {
	definition.verifyCalls++
	if definition.failVerify {
		return operation.Verification{Passed: false, Summary: "forced safe verification failure"}, nil
	}
	return operation.Verification{Passed: true, Summary: "safe verification passed"}, nil
}

func (definition *runtimeXRocketDefinition) Rollback(_ context.Context, input operation.RollbackInput) (operation.RollbackResult, error) {
	definition.rollbackCalls++
	if err := input.Lease.Validate(time.Now().UTC()); err != nil {
		return operation.RollbackResult{}, err
	}
	return operation.RollbackResult{
		Restored: true, MutationState: operation.MutationChanged, Checkpoint: "safe_rollback",
		State: operation.Artifact{SchemaVersion: "integration.xrocket.rollback.v1", Payload: json.RawMessage(`{"safe":true}`)},
	}, nil
}

func (definition *runtimeXRocketDefinition) VerifyRollback(context.Context, operation.VerifyRollbackInput) (operation.Verification, error) {
	definition.verifyRollbackCalls++
	return operation.Verification{Passed: true, Summary: "safe rollback verification passed"}, nil
}

type runtimeRestoreProvider struct{}

func (*runtimeRestoreProvider) ID() string { return "integration.xrocket.restore" }
func (provider *runtimeRestoreProvider) Create(_ context.Context, request operation.RestorePointRequest) (operation.RestorePoint, error) {
	return operation.RestorePoint{
		ID: "safe-" + request.RunID, ProviderID: provider.ID(), OperationID: request.OperationID, RunID: request.RunID,
		Status: operation.RestorePointVerified, Targets: append([]operation.Target(nil), request.Targets...), CreatedAt: time.Now().UTC(),
		Manifest: operation.Artifact{SchemaVersion: "integration.xrocket.restore.v1", Payload: json.RawMessage(`{"safe":true}`)},
	}, nil
}
func (*runtimeRestoreProvider) Verify(context.Context, operation.RestorePoint) (operation.Verification, error) {
	return operation.Verification{Passed: true, Summary: "safe restore point verified"}, nil
}
func (*runtimeRestoreProvider) Restore(context.Context, operation.RestorePoint, operation.ApplyResult) (operation.RollbackResult, error) {
	return operation.RollbackResult{}, errors.New("runtime userflow restore provider mutation is forbidden")
}
func (*runtimeRestoreProvider) VerifyRestored(context.Context, operation.RestorePoint, operation.RollbackResult) (operation.Verification, error) {
	return operation.Verification{Passed: true, Summary: "safe restore verification"}, nil
}

func runtimeManagementRequest(t *testing.T, handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
