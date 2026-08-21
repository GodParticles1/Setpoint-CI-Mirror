package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"setpoint/internal/protocol"
)

func TestRotateAndPersistCredentialSwitchesOnlyAfterSavingReplacement(t *testing.T) {
	replacement := newCredentialResponse(t)
	var activated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agents/agent-1/credentials/rotate":
			if request.Header.Get("Authorization") != "Bearer old-credential" {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(replacement)
		case "/api/v1/agents/agent-1/register":
			if request.Header.Get("Authorization") != "Bearer "+replacement.Secret {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			activated.Store(true)
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(protocol.RegistrationResponse{NodeID: "agent-1"})
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
	registration := protocol.RegistrationRequest{
		AgentID: "agent-1", Hostname: "node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test",
	}
	if err := RotateAndPersistCredential(context.Background(), config, client, registration); err != nil {
		t.Fatalf("rotate and persist: %v", err)
	}
	if client.Credential() != replacement.Secret {
		t.Fatal("client did not switch to replacement credential")
	}
	stored, found, err := LoadCredential(config.CredentialPath)
	if err != nil || !found || stored.Secret != replacement.Secret || stored.CredentialID != replacement.CredentialID {
		t.Fatal("replacement credential was not stored")
	}
	if !activated.Load() {
		t.Fatal("replacement credential was not activated")
	}
}

func TestPermanentAuthenticationErrorClassification(t *testing.T) {
	for _, code := range []string{"auth_invalid", "auth_expired", "auth_revoked", "auth_agent_mismatch"} {
		if !IsPermanentAuthenticationError(&APIError{Status: http.StatusUnauthorized, Code: code}) {
			t.Fatalf("code %q was not permanent", code)
		}
	}
	if IsPermanentAuthenticationError(&APIError{Status: http.StatusServiceUnavailable, Code: "unavailable"}) {
		t.Fatal("temporary availability failure was classified as permanent")
	}
}

func credentialResponse(id, secret string) protocol.AgentCredentialResponse {
	return protocol.AgentCredentialResponse{
		AgentID: "agent-1", CredentialID: id, Secret: secret,
		CreatedAt: time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC),
	}
}
