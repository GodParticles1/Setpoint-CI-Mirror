package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"setpoint/internal/protocol"
)

func TestClientRegistersAndSendsHeartbeat(t *testing.T) {
	var registered, heartbeat atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Header.Get("Authorization") != "Bearer credential-secret" {
			http.Error(writer, "missing credential", http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/api/v1/agents/agent-1/register":
			registered.Store(true)
			_ = json.NewEncoder(writer).Encode(protocol.RegistrationResponse{NodeID: "agent-1"})
		case "/api/v1/agents/agent-1/heartbeat":
			heartbeat.Store(true)
			_ = json.NewEncoder(writer).Encode(protocol.HeartbeatResponse{AgentID: "agent-1"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	client.SetCredential("credential-secret")
	if err := client.Register(context.Background(), protocol.RegistrationRequest{AgentID: "agent-1"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := client.Heartbeat(context.Background(), "agent-1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !registered.Load() || !heartbeat.Load() {
		t.Fatalf("requests missing: registered=%v heartbeat=%v", registered.Load(), heartbeat.Load())
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error":{"code":"not_found","message":"resource not found"}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	err = client.Heartbeat(context.Background(), "missing")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != http.StatusNotFound || apiError.Code != "not_found" {
		t.Fatalf("unexpected API error: %v", err)
	}
}
