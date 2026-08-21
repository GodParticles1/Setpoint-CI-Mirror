package integration

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"setpoint/internal/api"
)

type integrationHandlers struct {
	management http.Handler
	agent      http.Handler
}

func newIntegrationHandlers(t *testing.T, health api.HealthStore, service api.Service) integrationHandlers {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	management, err := api.NewManagementHandler(health, service, logger)
	if err != nil {
		t.Fatalf("new management handler: %v", err)
	}
	agent, err := api.NewAgentHandler(service, logger)
	if err != nil {
		t.Fatalf("new Agent handler: %v", err)
	}
	return integrationHandlers{management: management, agent: agent}
}
