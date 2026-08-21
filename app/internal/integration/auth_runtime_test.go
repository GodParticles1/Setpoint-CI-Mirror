package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/app"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
)

func TestHTTPEnrollmentCredentialPersistenceRotationAndRevocation(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	registry := plugin.NewCheckRegistry()
	service, err := app.NewService(store, store, registry, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handlers := newIntegrationHandlers(t, store, service)
	managementServer := httptest.NewServer(handlers.management)
	defer managementServer.Close()
	agentServer := httptest.NewServer(handlers.agent)
	defer agentServer.Close()

	enrollment := createEnrollmentToken(t, managementServer.Client(), managementServer.URL)
	config := agent.DefaultConfig()
	config.ServerURL = agentServer.URL
	config.CredentialPath = filepath.Join(t.TempDir(), "agent", "credential.json")
	config.EnrollmentToken = enrollment.Secret
	client, err := agent.NewClient(agentServer.URL, agentServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := agent.BootstrapCredential(ctx, config, client, "agent-auth-integration")
	if err != nil {
		t.Fatalf("bootstrap credential: %v", err)
	}
	credentialContents, err := os.ReadFile(config.CredentialPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(credentialContents, []byte(enrollment.Secret)) {
		t.Fatal("enrollment token leaked into Agent credential file")
	}

	registration := protocol.RegistrationRequest{
		AgentID: "agent-auth-integration", Hostname: "auth-node", OS: "linux",
		OSVersion: "test", Arch: "amd64", AgentVersion: "integration",
	}
	if err := client.Register(ctx, registration); err != nil {
		t.Fatalf("register with persisted credential: %v", err)
	}
	if err := client.Heartbeat(ctx, registration.AgentID); err != nil {
		t.Fatalf("heartbeat with persisted credential: %v", err)
	}

	if err := agent.RotateAndPersistCredential(ctx, config, client, registration); err != nil {
		t.Fatalf("rotate credential: %v", err)
	}
	oldClient, err := agent.NewClient(agentServer.URL, agentServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	oldClient.SetCredential(stored.Secret)
	err = oldClient.Heartbeat(ctx, registration.AgentID)
	var oldCredentialError *agent.APIError
	if !errors.As(err, &oldCredentialError) || oldCredentialError.Code != "auth_revoked" {
		t.Fatalf("activated rotation left old credential valid: %v", err)
	}
	replacement, found, err := agent.LoadCredential(config.CredentialPath)
	if err != nil || !found || replacement.CredentialID == stored.CredentialID {
		t.Fatal("rotated credential was not persisted")
	}
	restartedClient, err := agent.NewClient(agentServer.URL, agentServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	config.EnrollmentToken = ""
	if _, _, err := agent.BootstrapCredential(ctx, config, restartedClient, registration.AgentID); err != nil {
		t.Fatalf("reload replacement credential: %v", err)
	}
	if err := restartedClient.Heartbeat(ctx, registration.AgentID); err != nil {
		t.Fatalf("heartbeat after credential reload: %v", err)
	}

	response := integrationRequest(t, managementServer.Client(), http.MethodPost,
		managementServer.URL+"/api/v1/agent-credentials/"+replacement.CredentialID+"/revoke", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke credential status=%d body=%s", response.StatusCode, integrationBody(t, response))
	}
	_ = response.Body.Close()
	err = restartedClient.Heartbeat(ctx, registration.AgentID)
	var apiError *agent.APIError
	if !errors.As(err, &apiError) || apiError.Code != "auth_revoked" || !agent.IsPermanentAuthenticationError(err) {
		t.Fatalf("revoked credential error=%v", err)
	}
}

func createEnrollmentToken(t *testing.T, client *http.Client, baseURL string) protocol.EnrollmentTokenResponse {
	t.Helper()
	payload, _ := json.Marshal(protocol.CreateEnrollmentTokenRequest{
		APIVersion: "setpoint.io/v1", Kind: "EnrollmentToken",
	})
	response := integrationRequest(t, client, http.MethodPost, baseURL+"/api/v1/enrollment-tokens", payload, "")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create enrollment token status=%d body=%s", response.StatusCode, integrationBody(t, response))
	}
	var token protocol.EnrollmentTokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return token
}

func integrationRequest(t *testing.T, client *http.Client, method, endpoint string, body []byte, bearer string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func integrationBody(t *testing.T, response *http.Response) string {
	t.Helper()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return strings.TrimSpace(string(contents))
}
