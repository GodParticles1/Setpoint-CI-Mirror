package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"setpoint/internal/protocol"
)

func TestRotationActivationFailureKeepsReplacementForRetry(t *testing.T) {
	replacement := newCredentialResponse(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agents/agent-1/credentials/rotate":
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(replacement)
		case "/api/v1/agents/agent-1/register":
			http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.SetCredential("old-credential")
	config := DefaultConfig()
	config.CredentialPath = filepath.Join(t.TempDir(), "credential.json")
	registration := protocol.RegistrationRequest{AgentID: "agent-1"}
	if err := RotateAndPersistCredential(context.Background(), config, client, registration); err == nil {
		t.Fatal("activation failure was hidden")
	}
	stored, found, err := LoadCredential(config.CredentialPath)
	if err != nil || !found || stored.Secret != replacement.Secret ||
		client.Credential() != replacement.Secret {
		t.Fatal("replacement credential was not retained for activation retry")
	}
}
