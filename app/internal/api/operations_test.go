package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/app"
	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/operation/xrocketreaddress"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
)

func TestOperationsAPIContractAndStableGates(t *testing.T) {
	handler, _, service := newOperationsAPIHandler(t)
	list := managementRequest(t, handler, http.MethodGet, "/api/v1/operations", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), clickhouse.OperationID) || !strings.Contains(list.Body.String(), `"apply":false`) ||
		!strings.Contains(list.Body.String(), `"fields":[{"name":"host","type":"string"`) {
		t.Fatalf("catalog status=%d body=%s", list.Code, list.Body.String())
	}
	missing := managementRequest(t, handler, http.MethodGet, "/api/v1/operations/operation.missing", nil)
	if missing.Code != http.StatusNotFound || errorCode(t, missing.Body.Bytes()) != "not_found" {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}

	request := apiOperationRunRequest("api-run-1")
	created := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", request)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var runID string
	var createdBody struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	runID = createdBody.Metadata.ID
	duplicate := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", request)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}

	conflictRequest := apiOperationRunRequest("api-run-1")
	conflictRequest = bytes.Replace(conflictRequest, []byte(`"database":"db"`), []byte(`"database":"other"`), 1)
	conflict := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", conflictRequest)
	if conflict.Code != http.StatusConflict || errorCode(t, conflict.Body.Bytes()) != "operation_run_idempotency_conflict" {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	plainSecret := bytes.Replace(apiOperationRunRequest("api-secret"), []byte(`"host":"127.0.0.1"`), []byte(`"host":"127.0.0.1","password":"must-not-persist"`), 1)
	rejected := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", plainSecret)
	if rejected.Code != http.StatusBadRequest || errorCode(t, rejected.Body.Bytes()) != "invalid_request" || strings.Contains(rejected.Body.String(), "must-not-persist") {
		t.Fatalf("secret status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	nestedTypo := bytes.Replace(apiOperationRunRequest("api-typo"), []byte(`"host":"127.0.0.1"`), []byte(`"host":"127.0.0.1","securee":true`), 1)
	rejected = managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", nestedTypo)
	if rejected.Code != http.StatusBadRequest || errorCode(t, rejected.Body.Bytes()) != "invalid_request" {
		t.Fatalf("nested typo status=%d body=%s", rejected.Code, rejected.Body.String())
	}

	confirm := []byte(`{"idempotency_key":"confirm-1","plan_digest":"sha256:wrong"}`)
	confirmed := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs/"+runID+"/confirm", confirm)
	if confirmed.Code != http.StatusConflict || errorCode(t, confirmed.Body.Bytes()) != "operation_plan_digest_conflict" {
		t.Fatalf("confirm status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}

	listedTasks, err := service.ListTasks(context.Background())
	if err != nil || len(listedTasks) != 0 {
		t.Fatalf("management Check tasks leaked operation task: %#v err=%v", listedTasks, err)
	}
}

func TestOperationsAPIRejectsMalformedOversizedAndNonManagementAccess(t *testing.T) {
	handler, _, _ := newOperationsAPIHandler(t)
	malformed := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", []byte(`{"api_version":`))
	if malformed.Code != http.StatusBadRequest || errorCode(t, malformed.Body.Bytes()) != "invalid_json" {
		t.Fatalf("malformed status=%d body=%s", malformed.Code, malformed.Body.String())
	}
	oversizedBody := append([]byte(`{"api_version":"`), bytes.Repeat([]byte("x"), maxRequestBody+1)...)
	oversizedBody = append(oversizedBody, []byte(`"}`)...)
	oversized := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", oversizedBody)
	if oversized.Code != http.StatusRequestEntityTooLarge || errorCode(t, oversized.Body.Bytes()) != "request_too_large" {
		t.Fatalf("oversized status=%d body=%s", oversized.Code, oversized.Body.String())
	}

	_, store, service := newOperationsAPIHandler(t)
	agent, err := NewAgentHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/operations", nil)
	response := httptest.NewRecorder()
	agent.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("Agent listener exposed operations: status=%d", response.Code)
	}
	_ = store
}

func TestOperationsAPIExposesXRocketAndPersistsNormalizedParameters(t *testing.T) {
	handler, _, _ := newOperationsAPIHandler(t)
	catalog := managementRequest(t, handler, http.MethodGet, "/api/v1/operations/"+xrocketreaddress.OperationID, nil)
	if catalog.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	var definition struct {
		Metadata operation.Metadata `json:"metadata"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &definition); err != nil {
		t.Fatal(err)
	}
	if definition.Metadata.ID != xrocketreaddress.OperationID || len(definition.Metadata.Parameters) != 5 {
		t.Fatalf("definition=%#v", definition)
	}

	request := protocol.CreateOperationRunRequest{APIVersion: "setpoint.io/v1", Kind: "OperationRun"}
	request.Metadata.IdempotencyKey = "xrocket-api-run"
	request.Spec.OperationID = xrocketreaddress.OperationID
	request.Spec.NodeID = "node-1"
	request.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	request.Spec.Parameters = json.RawMessage(`{"master_target_address":" 198.51.100.10 ","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","prefix_length":"24","gateway_address":"198.51.100.1"}`)
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	created := managementRequest(t, handler, http.MethodPost, "/api/v1/operation-runs", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var run struct {
		Spec struct {
			OperationID string          `json:"operation_id"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Spec.OperationID != xrocketreaddress.OperationID || string(run.Spec.Parameters) != `{"gateway_address":"198.51.100.1","master_target_address":"198.51.100.10","prefix_length":24,"slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12"}` {
		t.Fatalf("run spec=%#v", run.Spec)
	}
}

func newOperationsAPIHandler(t *testing.T) (http.Handler, *storage.Store, *app.Service) {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if _, err := store.RegisterNode(context.Background(), domain.Registration{AgentID: "node-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	checks := plugin.NewCheckRegistry()
	service, err := app.NewService(store, store, checks, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operations := operation.NewRegistry()
	if err := operations.Register(clickhouse.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	if err := operations.Register(xrocketreaddress.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	operationService, err := app.NewOperationsService(store, store, operations, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewManagementHandlerWithOperations(store, service, operationService, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler, store, service
}

func managementRequest(t *testing.T, handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
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

func apiOperationRunRequest(key string) []byte {
	request := protocol.CreateOperationRunRequest{}
	request.APIVersion = "setpoint.io/v1"
	request.Kind = "OperationRun"
	request.Metadata.IdempotencyKey = key
	request.Spec.OperationID = clickhouse.OperationID
	request.Spec.NodeID = "node-1"
	request.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	request.Spec.Parameters = json.RawMessage(`{"source":{"host":"127.0.0.1"},"target":{"host":"127.0.0.2"},"database":"db","tables":["table"]}`)
	encoded, _ := json.Marshal(request)
	return encoded
}
