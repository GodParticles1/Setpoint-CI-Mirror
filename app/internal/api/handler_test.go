package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/app"
	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
)

func TestAgentAndQueryAPI(t *testing.T) {
	handler := newTestHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	credential := enrollCredential(t, server.Client(), server.URL, "00000000-0000-4000-8000-000000000001")

	registration := protocol.RegistrationRequest{
		AgentID: "00000000-0000-4000-8000-000000000001", Hostname: "node-1",
		OS: "linux", OSVersion: "debian-12", Arch: "amd64", AgentVersion: "test",
	}
	body, _ := json.Marshal(registration)
	response := authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+registration.AgentID+"/register", body, credential.Secret)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+registration.AgentID+"/heartbeat", nil, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/nodes", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("nodes status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var nodesEnvelope struct {
		Nodes []domain.Node `json:"nodes"`
	}
	nodesBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read nodes: %v", err)
	}
	_ = response.Body.Close()
	if err := json.Unmarshal(nodesBody, &nodesEnvelope); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if len(nodesEnvelope.Nodes) != 1 || nodesEnvelope.Nodes[0].ID != registration.AgentID || nodesEnvelope.Nodes[0].Status != domain.NodeStatusOnline {
		t.Fatalf("unexpected nodes: %#v", nodesEnvelope.Nodes)
	}
	if bytes.Contains(nodesBody, []byte(`"reported_address"`)) || !bytes.Contains(nodesBody, []byte(`"observed_source_address"`)) {
		t.Fatalf("unexpected source address contract: %s", nodesBody)
	}
	observed := net.ParseIP(nodesEnvelope.Nodes[0].ObservedSourceAddress)
	if observed == nil || !observed.IsLoopback() {
		t.Fatalf("observed source address=%q", nodesEnvelope.Nodes[0].ObservedSourceAddress)
	}

	response = request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/checks", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("checks status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var checksEnvelope struct {
		Checks []plugin.Metadata `json:"checks"`
	}
	if err := json.NewDecoder(response.Body).Decode(&checksEnvelope); err != nil {
		t.Fatalf("decode checks: %v", err)
	}
	_ = response.Body.Close()
	if len(checksEnvelope.Checks) != len(plugins.Formal())+1 || checksEnvelope.Checks[0].ID != "dev.system-info" {
		t.Fatalf("unexpected checks: %#v", checksEnvelope.Checks)
	}
}

func TestAPIValidationAndUnknownHeartbeat(t *testing.T) {
	handler := newTestHandler(t)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/invalid/register", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || errorCode(t, response.Body.Bytes()) != "invalid_request" {
		t.Fatalf("invalid registration status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/agents/missing/heartbeat", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || errorCode(t, response.Body.Bytes()) != "auth_missing" {
		t.Fatalf("unknown heartbeat status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHealthAPI(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentRuntimeReadinessIsAgentListenerOnly(t *testing.T) {
	handlers := newTestHandlers(t)
	request := httptest.NewRequest(http.MethodGet, protocol.AgentRuntimeReadyPath, nil)
	response := httptest.NewRecorder()
	handlers.agent.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Agent readiness status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var ready protocol.AgentRuntimeReadyResponse
	if err := json.Unmarshal(response.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ok" || ready.Service != protocol.AgentRuntimeReadyService || ready.ContractVersion != protocol.AgentRuntimeContractVersion {
		t.Fatalf("unexpected Agent readiness: %#v", ready)
	}

	request = httptest.NewRequest(http.MethodGet, protocol.AgentRuntimeReadyPath, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	response = httptest.NewRecorder()
	handlers.management.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("management listener exposed Agent readiness: status=%d body=%s", response.Code, response.Body.String())
	}
}

type testHandlers struct {
	management http.Handler
	agent      http.Handler
}

func (handlers testHandlers) combined() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		if path == "/api/v1/agents/enroll" ||
			(strings.HasPrefix(path, "/api/v1/agents/") &&
				(strings.Contains(path, "/register") || strings.Contains(path, "/heartbeat") ||
					strings.Contains(path, "/credentials/rotate") || strings.Contains(path, "/tasks/"))) {
			handlers.agent.ServeHTTP(writer, request)
			return
		}
		handlers.management.ServeHTTP(writer, request)
	})
}

func newTestHandler(t *testing.T) http.Handler {
	return newTestHandlers(t).combined()
}

func newTestHandlers(t *testing.T) testHandlers {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registry := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(registry); err != nil {
		t.Fatalf("register formal checks: %v", err)
	}
	for _, candidate := range plugin.DevelopmentChecks() {
		if err := registry.Register(candidate); err != nil {
			t.Fatalf("register check: %v", err)
		}
	}
	service, err := app.NewService(store, store, registry, time.Minute)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := service.SyncChecks(context.Background()); err != nil {
		t.Fatalf("sync checks: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	management, err := NewManagementHandler(store, service, logger)
	if err != nil {
		t.Fatalf("new management handler: %v", err)
	}
	agent, err := NewAgentHandler(service, logger)
	if err != nil {
		t.Fatalf("new Agent handler: %v", err)
	}
	return testHandlers{management: management, agent: agent}
}

func request(t *testing.T, client *http.Client, method, url string, body []byte, contentType string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	return response
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_ = response.Body.Close()
	return string(contents)
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return envelope.Error.Code
}
