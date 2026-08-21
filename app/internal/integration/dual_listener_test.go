package integration

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/api"
	"setpoint/internal/app"
	"setpoint/internal/plugin"
	"setpoint/internal/server"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/webui"
)

func TestRealServerSeparatesManagementUIAndAgentListeners(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	service, err := app.NewService(store, store, plugin.NewCheckRegistry(), time.Minute)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	managementAPI, err := api.NewManagementHandler(store, service, logger)
	if err != nil {
		t.Fatalf("new management API: %v", err)
	}
	managementUI, err := webui.New(managementAPI)
	if err != nil {
		t.Fatalf("new management UI: %v", err)
	}
	agentHandler, err := api.NewAgentHandler(service, logger)
	if err != nil {
		t.Fatalf("new Agent handler: %v", err)
	}
	config := server.DefaultConfig()
	config.ShutdownTimeout = time.Second
	httpServer, err := server.New(config, api.ProtectManagement(managementUI), agentHandler, logger)
	if err != nil {
		t.Fatalf("new HTTP server: %v", err)
	}
	managementListener := integrationListener(t)
	agentListener := integrationListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	stopped := false
	go func() { result <- httpServer.Serve(ctx, managementListener, agentListener) }()
	t.Cleanup(func() {
		if stopped {
			return
		}
		cancel()
		select {
		case <-result:
		case <-time.After(2 * time.Second):
		}
	})

	managementURL := "http://" + managementListener.Addr().String()
	agentURL := "http://" + agentListener.Addr().String()
	response, err := http.Get(managementURL + "/")
	if err != nil {
		t.Fatalf("get management UI: %v", err)
	}
	body := integrationBody(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `id="root"`) {
		t.Fatalf("management UI status=%d body=%q", response.StatusCode, body)
	}

	assertHTTPStatus(t, agentURL+"/", http.StatusNotFound)
	assertHTTPStatus(t, agentURL+"/api/v1/sites", http.StatusNotFound)
	assertHTTPStatus(t, managementURL+"/api/v1/agents/test/heartbeat", http.StatusNotFound)

	enrollment := createEnrollmentToken(t, http.DefaultClient, managementURL)
	client, err := agent.NewClient(agentURL, http.DefaultClient)
	if err != nil {
		t.Fatalf("new Agent client: %v", err)
	}
	agentConfig := agent.DefaultConfig()
	agentConfig.ServerURL = agentURL
	agentConfig.CredentialPath = filepath.Join(t.TempDir(), "agent-credential.json")
	agentConfig.EnrollmentToken = enrollment.Secret
	if _, _, err := agent.BootstrapCredential(context.Background(), agentConfig, client, "dual-listener-agent"); err != nil {
		t.Fatalf("enroll through Agent listener: %v", err)
	}

	cancel()
	select {
	case err := <-result:
		stopped = true
		if err != nil {
			t.Fatalf("stop dual-listener server: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dual-listener server did not stop")
	}
}

func integrationListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

func assertHTTPStatus(t *testing.T, endpoint string, want int) {
	t.Helper()
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatalf("get %s: %v", endpoint, err)
	}
	_ = response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("get %s status=%d want=%d", endpoint, response.StatusCode, want)
	}
}
