package integration

import (
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
	"setpoint/internal/app"
	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	storage "setpoint/internal/storage/sqlite"
)

func TestAgentRegistersHeartbeatsAndStopsAgainstRealServerStack(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registry := plugin.NewCheckRegistry()
	service, err := app.NewService(store, store, registry, time.Second)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers := newIntegrationHandlers(t, store, service)
	managementServer := httptest.NewServer(handlers.management)
	defer managementServer.Close()
	agentServer := httptest.NewServer(handlers.agent)
	defer agentServer.Close()

	client, err := agent.NewClient(agentServer.URL, agentServer.Client())
	if err != nil {
		t.Fatalf("new agent client: %v", err)
	}
	provisionAgentCredential(t, store, client, "00000000-0000-4000-8000-000000000001")
	config := agent.DefaultConfig()
	config.ServerURL = agentServer.URL
	config.HeartbeatInterval = 10 * time.Millisecond
	config.RetryInitialDelay = time.Millisecond
	config.RetryMaxDelay = 2 * time.Millisecond
	runner, err := agent.NewRunner(config, client, noopTaskProcessor{}, "00000000-0000-4000-8000-000000000001", "integration",
		agent.SystemInfo{Hostname: "node-1", OS: "linux", OSVersion: "test", Arch: "amd64"}, logger)
	if err != nil {
		t.Fatalf("new agent runner: %v", err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(runContext) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		nodes := fetchNodes(t, managementServer.Client(), managementServer.URL)
		if len(nodes) == 1 && nodes[0].ID == "00000000-0000-4000-8000-000000000001" && nodes[0].Status == domain.NodeStatusOnline && nodes[0].LastSeenAt.After(nodes[0].RegisteredAt) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("agent did not register and heartbeat before deadline: %#v", nodes)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("agent runner after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent runner did not stop after cancellation")
	}
}

func fetchNodes(t *testing.T, client *http.Client, baseURL string) []domain.Node {
	t.Helper()
	response, err := client.Get(baseURL + "/api/v1/nodes")
	if err != nil {
		t.Fatalf("get nodes: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get nodes status: %d", response.StatusCode)
	}
	var envelope struct {
		Nodes []domain.Node `json:"nodes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	return envelope.Nodes
}
