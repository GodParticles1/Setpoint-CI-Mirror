package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestNewAppliesHTTPBoundsToBothListeners(t *testing.T) {
	config := DefaultConfig()
	config.MaxHeaderBytes = 256 * 1024
	server, err := New(config, http.NotFoundHandler(), http.NotFoundHandler(), testLogger())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	if server.management.MaxHeaderBytes != config.MaxHeaderBytes || server.agent.MaxHeaderBytes != config.MaxHeaderBytes {
		t.Fatalf("management=%d Agent=%d want=%d", server.management.MaxHeaderBytes, server.agent.MaxHeaderBytes, config.MaxHeaderBytes)
	}
}

func TestServeRunsDistinctListenersAndStopsAfterContextCancellation(t *testing.T) {
	config := DefaultConfig()
	config.ShutdownTimeout = time.Second
	managementHandler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Setpoint-Listener", "management")
		writer.WriteHeader(http.StatusNoContent)
	})
	agentHandler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Setpoint-Listener", "agent")
		writer.WriteHeader(http.StatusNoContent)
	})
	server, err := New(config, managementHandler, agentHandler, testLogger())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	managementListener := listenLoopback(t)
	agentListener := listenLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx, managementListener, agentListener) }()

	for name, listener := range map[string]net.Listener{"management": managementListener, "agent": agentListener} {
		response, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			cancel()
			t.Fatalf("request %s listener: %v", name, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent || response.Header.Get("X-Setpoint-Listener") != name {
			cancel()
			t.Fatalf("listener=%s status=%d marker=%q", name, response.StatusCode, response.Header.Get("X-Setpoint-Listener"))
		}
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop both listeners after cancellation")
	}
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
