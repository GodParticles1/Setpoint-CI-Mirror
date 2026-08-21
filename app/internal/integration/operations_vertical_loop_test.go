package integration

import (
	"bytes"
	"context"
	"encoding/json"
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
	"setpoint/internal/operationrun"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

func TestOperationsPlanningVerticalLoopPersistsAgentResult(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "operation-agent", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	checks := plugin.NewCheckRegistry()
	service, err := app.NewService(store, store, checks, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	definition := &integrationPlanningDefinition{}
	operations := operation.NewRegistry()
	if err := operations.Register(definition); err != nil {
		t.Fatal(err)
	}
	operationService, err := app.NewOperationsService(store, store, operations, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	management, err := api.NewManagementHandlerWithOperations(store, service, operationService, logger)
	if err != nil {
		t.Fatal(err)
	}

	var request operationRunRequestFixture
	request.APIVersion = "setpoint.io/v1"
	request.Kind = "OperationRun"
	request.Metadata.IdempotencyKey = "run-idem"
	request.Spec.OperationID = definition.Metadata().ID
	request.Spec.NodeID = "operation-agent"
	request.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "operation-agent"}}
	request.Spec.Parameters = json.RawMessage(`{}`)
	requestBody, err := json.Marshal(request.protocol())
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/api/v1/operation-runs", bytes.NewReader(requestBody))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.RemoteAddr = "127.0.0.1:12345"
	httpRequest.Host = "127.0.0.1:8080"
	httpResponse := httptest.NewRecorder()
	management.ServeHTTP(httpResponse, httpRequest)
	if httpResponse.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", httpResponse.Code, httpResponse.Body.String())
	}
	var run operationrun.Resource
	if err := json.Unmarshal(httpResponse.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Status.State != operation.StateDraft || run.Status.TaskID == "" {
		t.Fatalf("created run=%#v", run)
	}

	remote := &storeTaskRemote{service: service}
	journal, err := agent.NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := agent.NewTaskWorkerWithOperations(remote, "operation-agent", "linux", checks, operations, integrationOperationExecutor{}, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	getRequest := httptest.NewRequest(http.MethodGet, "/api/v1/operation-runs/"+run.Metadata.ID, nil)
	getRequest.RemoteAddr = "127.0.0.1:12345"
	getRequest.Host = "127.0.0.1:8080"
	getResponse := httptest.NewRecorder()
	management.ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	var stored operationrun.Resource
	if err := json.Unmarshal(getResponse.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.State != operation.StateAwaitingConfirm || stored.PlanDigest == "" || stored.Plan == nil || stored.Impact == nil {
		t.Fatalf("stored=%#v", stored)
	}
	if stored.Status.ApplyAvailable {
		t.Fatal("vertical planning loop enabled Product Apply")
	}
}

type operationRunRequestFixture struct {
	APIVersion string
	Kind       string
	Metadata   struct{ IdempotencyKey string }
	Spec       struct {
		OperationID string
		NodeID      string
		Targets     []operation.Target
		Parameters  json.RawMessage
	}
}

func (fixture operationRunRequestFixture) protocol() (request protocol.CreateOperationRunRequest) {
	request.APIVersion = fixture.APIVersion
	request.Kind = fixture.Kind
	request.Metadata.IdempotencyKey = fixture.Metadata.IdempotencyKey
	request.Spec.OperationID = fixture.Spec.OperationID
	request.Spec.NodeID = fixture.Spec.NodeID
	request.Spec.Targets = fixture.Spec.Targets
	request.Spec.Parameters = fixture.Spec.Parameters
	return request
}

type storeTaskRemote struct{ service *app.Service }

func (remote *storeTaskRemote) ClaimTask(ctx context.Context, agentID string) (*task.Resource, error) {
	return remote.service.ClaimTask(ctx, agentID)
}
func (remote *storeTaskRemote) AcknowledgeTask(ctx context.Context, agentID, taskID, claimID string) (task.Resource, error) {
	return remote.service.AcknowledgeTask(ctx, agentID, taskID, protocol.AcknowledgeTaskRequest{ClaimID: claimID})
}
func (remote *storeTaskRemote) SubmitTaskResult(ctx context.Context, agentID, taskID string, submission task.ResultSubmission) (task.Resource, error) {
	return remote.service.SubmitTaskResult(ctx, agentID, taskID, submission)
}

type integrationPlanningDefinition struct{}

func (*integrationPlanningDefinition) Metadata() operation.Metadata {
	return operation.Metadata{ID: "operation.integration.plan", Category: "test", Name: "Integration plan", Version: "1.0.0", Risk: operation.RiskLow, Impact: "none", SupportedSystems: []string{"linux"}}
}
func (*integrationPlanningDefinition) Discover(_ context.Context, input operation.DiscoverInput) (operation.Discovery, error) {
	return operation.Discovery{Applicable: true, Summary: "discovered", Targets: input.Runtime.Targets, Snapshot: operation.Artifact{SchemaVersion: "integration.discovery.v1", Payload: json.RawMessage(`{}`)}}, nil
}
func (*integrationPlanningDefinition) Precheck(context.Context, operation.PrecheckInput) (operation.Precheck, error) {
	return operation.Precheck{Passed: true, Summary: "passed", Snapshot: operation.Artifact{SchemaVersion: "integration.precheck.v1", Payload: json.RawMessage(`{}`)}}, nil
}
func (*integrationPlanningDefinition) Plan(context.Context, operation.PlanInput) (operation.Plan, error) {
	return operation.Plan{SchemaVersion: "integration.plan.v1", Summary: "plan", Steps: []operation.PlanStep{}, Execution: operation.Artifact{SchemaVersion: "integration.execution.v1", Payload: json.RawMessage(`{}`)}}, nil
}
func (*integrationPlanningDefinition) Impact(context.Context, operation.ImpactInput) (operation.Impact, error) {
	return operation.Impact{Summary: "impact", Risk: operation.RiskLow, Changes: []operation.Change{}}, nil
}

type integrationOperationExecutor struct{}

func (integrationOperationExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	return executor.Result{}, nil
}
