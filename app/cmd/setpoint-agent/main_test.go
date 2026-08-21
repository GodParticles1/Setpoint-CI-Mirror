package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeProbeExitsBeforeAgentStateCreation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","service":"setpoint-agent-listener","contract_version":"v1"}`))
	}))
	defer server.Close()

	working := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(working); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run([]string{"runtime-probe", "--server-url", server.URL}, logger); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"data", "agent-id", "agent-credential.json", "task-journal.json", "enrollment-token"} {
		if _, err := os.Lstat(filepath.Join(working, candidate)); !os.IsNotExist(err) {
			t.Fatalf("runtime probe created Agent state %q: %v", candidate, err)
		}
	}
}

func TestRuntimeProbeRequiresExplicitServerURL(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run([]string{"runtime-probe"}, logger); err == nil {
		t.Fatal("runtime probe accepted missing server URL")
	}
}

func TestRuntimeProbeRejectsAdditionalArguments(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run([]string{"runtime-probe", "--server-url", "http://127.0.0.1:8081", "extra"}, logger); err == nil {
		t.Fatal("runtime probe accepted additional arguments")
	}
}
