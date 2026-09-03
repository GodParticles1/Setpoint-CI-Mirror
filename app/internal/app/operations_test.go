package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

func TestOperationsServiceCatalogCreateIdempotencyAndConfirmGate(t *testing.T) {
	ctx := context.Background()
	store, service := newOperationsServiceTest(t)
	request := validOperationRunRequest()
	run, created, err := service.CreateOperationRun(ctx, request)
	if err != nil || !created {
		t.Fatalf("create run created=%v err=%v", created, err)
	}
	if run.Spec.OperationVersion != clickhouse.OperationMetadata().Version || run.Spec.CapabilityDigest == "" || run.Status.ApplyAvailable || run.Status.State != operation.StateDraft {
		t.Fatalf("frozen run=%#v", run)
	}
	duplicate, created, err := service.CreateOperationRun(ctx, request)
	if err != nil || created || duplicate.Metadata.ID != run.Metadata.ID {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	semanticDuplicate := validOperationRunRequest()
	semanticDuplicate.Spec.Parameters = json.RawMessage(`{"tables":["table"],"database":"db","target":{"port":9000,"host":"127.0.0.2"},"source":{"port":9000,"host":"127.0.0.1"}}`)
	duplicate, created, err = service.CreateOperationRun(ctx, semanticDuplicate)
	if err != nil || created || duplicate.Metadata.ID != run.Metadata.ID {
		t.Fatalf("semantic duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	request.Spec.Parameters = json.RawMessage(`{"source":{"host":"127.0.0.3"},"target":{"host":"127.0.0.2"},"database":"db","tables":["table"]}`)
	if _, _, err := service.CreateOperationRun(ctx, request); !errors.Is(err, ErrOperationRunIdempotencyConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}

	taskResource, err := store.GetTask(ctx, run.Status.TaskID)
	if err != nil || taskResource.Kind != "OperationPlanningTask" || taskResource.Spec.OperationID != run.Spec.OperationID {
		t.Fatalf("planning task=%#v err=%v", taskResource, err)
	}
	if _, err := service.ConfirmOperationRun(ctx, run.Metadata.ID, protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-1", PlanDigest: "sha256:wrong"}); !errors.Is(err, ErrOperationPlanDigestConflict) {
		t.Fatalf("plan conflict=%v", err)
	}
	if _, err := service.ConfirmOperationRun(ctx, run.Metadata.ID, protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-1", PlanDigest: ""}); !errors.Is(err, ErrOperationPlanDigestConflict) {
		t.Fatalf("empty plan conflict=%v", err)
	}
	claimed, err := store.ClaimTask(ctx, "node-1", "claim-op", time.Now().UTC())
	if err != nil || claimed == nil {
		t.Fatalf("claim planning task=%#v err=%v", claimed, err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-1", claimed.Metadata.ID, "claim-op", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	plan := operation.Plan{SchemaVersion: "test.v1", Summary: "plan", Steps: []operation.PlanStep{}, Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}
	impact := operation.Impact{Summary: "impact", Risk: operation.RiskHigh, Changes: []operation.Change{}}
	planningResult := operation.PlanningResult{OperationID: run.Spec.OperationID, OperationVersion: run.Spec.OperationVersion,
		CapabilityDigest: run.Spec.CapabilityDigest, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready",
		StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC().Add(time.Second), Plan: &plan, Impact: &impact, PlanDigest: "sha256:plan"}
	if _, err := store.CompleteTask(ctx, "node-1", claimed.Metadata.ID, task.ResultSubmission{ClaimID: "claim-op", Phase: task.PhaseSucceeded, OperationResult: &planningResult}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmOperationRun(ctx, run.Metadata.ID, protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-2", PlanDigest: "sha256:plan"}); !errors.Is(err, ErrProductApplyDisabled) {
		t.Fatalf("product Apply gate=%v", err)
	}
	canceled, err := service.CancelOperationRun(ctx, run.Metadata.ID)
	if err != nil || canceled.Status.State != operation.StateCanceledBeforeApply {
		t.Fatalf("cancel=%#v err=%v", canceled, err)
	}
	if _, err := service.ConfirmOperationRun(ctx, run.Metadata.ID, protocol.ConfirmOperationRunRequest{IdempotencyKey: "confirm-3", PlanDigest: "sha256:plan"}); !errors.Is(err, ErrOperationStateConflict) {
		t.Fatalf("state conflict=%v", err)
	}

	definitions, err := service.ListOperations()
	if err != nil || len(definitions) != 1 || definitions[0].Availability.Apply || !definitions[0].Availability.Planning {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	definition, err := service.GetOperation(clickhouse.OperationID)
	if err != nil || definition.CapabilityDigest != run.Spec.CapabilityDigest {
		t.Fatalf("definition=%#v err=%v", definition, err)
	}
}

func TestOperationsServiceRejectsTargetsParametersAndPlaintextSecrets(t *testing.T) {
	_, service := newOperationsServiceTest(t)
	tests := []struct {
		name   string
		mutate func(*protocol.CreateOperationRunRequest)
	}{
		{name: "missing target", mutate: func(request *protocol.CreateOperationRunRequest) { request.Spec.Targets = nil }},
		{name: "wrong node target", mutate: func(request *protocol.CreateOperationRunRequest) { request.Spec.Targets[0].NodeID = "other-node" }},
		{name: "missing required parameter", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.Parameters = json.RawMessage(`{"source":{},"target":{},"database":"db"}`)
		}},
		{name: "wrong parameter type", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.Parameters = json.RawMessage(`{"source":{},"target":{},"database":1,"tables":["t"]}`)
		}},
		{name: "undeclared parameter", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.Parameters = json.RawMessage(`{"source":{},"target":{},"database":"db","tables":["t"],"strategy":"native"}`)
		}},
		{name: "nested password", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.Parameters = json.RawMessage(`{"source":{"password":"do-not-store"},"target":{},"database":"db","tables":["t"]}`)
		}},
		{name: "nested array api key", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.Parameters = json.RawMessage(`{"source":{"options":[{"api_key":"do-not-store"}]},"target":{},"database":"db","tables":["t"]}`)
		}},
		{name: "nested unknown endpoint field", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.Parameters = json.RawMessage(`{"source":{"host":"127.0.0.1","passwrod":"do-not-store"},"target":{"host":"127.0.0.2"},"database":"db","tables":["t"]}`)
		}},
		{name: "bad secret ref", mutate: func(request *protocol.CreateOperationRunRequest) {
			request.Spec.SecretRefs = []operation.SecretRef{{RequirementID: "unknown", Reference: "ref-1"}}
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			request := validOperationRunRequest()
			request.Metadata.IdempotencyKey += "-" + testCase.name
			testCase.mutate(&request)
			if _, _, err := service.CreateOperationRun(context.Background(), request); !IsValidationError(err) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOperationRunFreezesExactlyTwoParticipants(t *testing.T) {
	store, service := newOperationsServiceTest(t)
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	if _, err := store.RegisterNode(context.Background(), domain.Registration{AgentID: "node-2", Hostname: "node-2", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := validOperationRunRequest()
	request.Metadata.IdempotencyKey = "operation-two-participants"
	request.Spec.ParticipantNodeIDs = []string{"node-2", "node-1"}
	request.Spec.Targets = append(request.Spec.Targets, operation.Target{Kind: operation.TargetNode, NodeID: "node-2"})
	run, created, err := service.CreateOperationRun(context.Background(), request)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if len(run.Spec.ParticipantNodeIDs) != 2 || run.Spec.ParticipantNodeIDs[0] != "node-1" || run.Spec.ParticipantNodeIDs[1] != "node-2" {
		t.Fatalf("participants=%v", run.Spec.ParticipantNodeIDs)
	}
	restored, err := store.GetOperationRun(context.Background(), run.Metadata.ID)
	if err != nil || !reflect.DeepEqual(restored.Spec.ParticipantNodeIDs, run.Spec.ParticipantNodeIDs) {
		t.Fatalf("restored participants=%v err=%v", restored.Spec.ParticipantNodeIDs, err)
	}
}

func TestSingleNodeOperationFreezesImplicitParticipantWithoutChangingTaskNode(t *testing.T) {
	store, service := newOperationsServiceTest(t)
	request := validOperationRunRequest()
	run, _, err := service.CreateOperationRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(run.Spec.ParticipantNodeIDs, []string{"node-1"}) {
		t.Fatalf("participants=%v", run.Spec.ParticipantNodeIDs)
	}
	planning, err := store.GetTask(context.Background(), run.Status.TaskID)
	if err != nil || planning.Spec.NodeID != "node-1" {
		t.Fatalf("planning=%#v err=%v", planning, err)
	}
}

func newOperationsServiceTest(t *testing.T) (*storage.Store, *OperationsService) {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	if _, err := store.RegisterNode(context.Background(), domain.Registration{AgentID: "node-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(clickhouse.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	service, err := NewOperationsService(store, store, registry, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return store, service
}

func validOperationRunRequest() protocol.CreateOperationRunRequest {
	var request protocol.CreateOperationRunRequest
	request.APIVersion = "setpoint.io/v1"
	request.Kind = "OperationRun"
	request.Metadata.IdempotencyKey = "operation-create-1"
	request.Spec.OperationID = clickhouse.OperationID
	request.Spec.NodeID = "node-1"
	request.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	request.Spec.Parameters = json.RawMessage(`{"source":{"host":"127.0.0.1"},"target":{"host":"127.0.0.2"},"database":"db","tables":["table"]}`)
	return request
}
