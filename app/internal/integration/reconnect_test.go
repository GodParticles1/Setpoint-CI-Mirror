package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/app"
	"setpoint/internal/plugin"
	storage "setpoint/internal/storage/sqlite"
)

func TestAgentReregistersAndResumesHeartbeatAfterServerRecovery(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			_ = store.Close()
		}
	})
	registry := plugin.NewCheckRegistry()
	service, err := app.NewService(store, store, registry, time.Second)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers := newIntegrationHandlers(t, store, service)
	managementServer := httptest.NewServer(handlers.management)
	defer managementServer.Close()
	gate := &availabilityHandler{next: handlers.agent}
	gate.setAvailable(true)
	agentServer := httptest.NewServer(gate)
	serverClosed := false
	t.Cleanup(func() {
		if !serverClosed {
			agentServer.Close()
		}
	})

	client, err := agent.NewClient(agentServer.URL, &http.Client{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("new agent client: %v", err)
	}
	provisionAgentCredential(t, store, client, "00000000-0000-4000-8000-000000000002")
	config := agent.DefaultConfig()
	config.ServerURL = agentServer.URL
	config.HeartbeatInterval = 5 * time.Millisecond
	config.RequestTimeout = 100 * time.Millisecond
	config.RetryMaxAttempts = 2
	config.RetryInitialDelay = 2 * time.Millisecond
	config.RetryMaxDelay = 4 * time.Millisecond
	config.ReconnectInitialDelay = 2 * time.Millisecond
	config.ReconnectMaxDelay = 8 * time.Millisecond
	runner, err := agent.NewRunner(config, client, noopTaskProcessor{}, "00000000-0000-4000-8000-000000000002", "integration",
		agent.SystemInfo{Hostname: "node-reconnect", OS: "linux", OSVersion: "test", Arch: "amd64"}, logger)
	if err != nil {
		t.Fatalf("new agent runner: %v", err)
	}
	runContext, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	var runnerStopped atomic.Bool
	go func() { result <- runner.Run(runContext) }()
	t.Cleanup(func() {
		if runnerStopped.Load() {
			return
		}
		cancel()
		select {
		case <-result:
			runnerStopped.Store(true)
		case <-time.After(time.Second):
		}
	})

	waitForCondition(t, 2*time.Second, func() bool {
		nodes := fetchNodes(t, managementServer.Client(), managementServer.URL)
		return len(nodes) == 1 && nodes[0].LastSeenAt.After(nodes[0].RegisteredAt)
	}, "initial registration and heartbeat")
	before := fetchNodes(t, managementServer.Client(), managementServer.URL)[0].LastSeenAt
	initialRegistrations := gate.registrations.Load()

	gate.setAvailable(false)
	waitForCondition(t, 2*time.Second, func() bool {
		return gate.unavailableRegistrations.Load() > 0
	}, "registration attempt while server is unavailable")
	gate.setAvailable(true)

	waitForCondition(t, 2*time.Second, func() bool {
		nodes := fetchNodes(t, managementServer.Client(), managementServer.URL)
		return gate.registrations.Load() > initialRegistrations && len(nodes) == 1 && nodes[0].LastSeenAt.After(before)
	}, "registration and heartbeat after server recovery")

	gate.disableAndWait()
	cancel()
	select {
	case err := <-result:
		runnerStopped.Store(true)
		if err != nil {
			t.Fatalf("agent runner after cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent runner did not stop after cancellation")
	}
	agentServer.Close()
	serverClosed = true
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	storeClosed = true
}

type availabilityHandler struct {
	next                     http.Handler
	mu                       sync.RWMutex
	available                bool
	inFlight                 sync.WaitGroup
	registrations            atomic.Int32
	unavailableRegistrations atomic.Int32
}

func (handler *availabilityHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	isRegistration := strings.HasPrefix(request.URL.Path, "/api/v1/agents/") && strings.HasSuffix(request.URL.Path, "/register")
	handler.mu.RLock()
	if !handler.available {
		handler.mu.RUnlock()
		if isRegistration {
			handler.unavailableRegistrations.Add(1)
		}
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	handler.inFlight.Add(1)
	handler.mu.RUnlock()
	defer handler.inFlight.Done()
	if isRegistration {
		handler.registrations.Add(1)
	}
	handler.next.ServeHTTP(writer, request)
}

func (handler *availabilityHandler) setAvailable(available bool) {
	handler.mu.Lock()
	handler.available = available
	handler.mu.Unlock()
}

func (handler *availabilityHandler) disableAndWait() {
	handler.setAvailable(false)
	handler.inFlight.Wait()
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
