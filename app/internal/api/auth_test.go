package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"setpoint/internal/auth"
	"setpoint/internal/protocol"
)

func TestEnrollmentRotationRevocationAndAuthenticationErrors(t *testing.T) {
	handler := newTestHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	credential := enrollCredential(t, server.Client(), server.URL, "agent-auth")
	registration := protocol.RegistrationRequest{
		AgentID: "agent-auth", Hostname: "node-auth", OS: "linux",
		OSVersion: "test", Arch: "amd64", AgentVersion: "test",
	}
	body, _ := json.Marshal(registration)

	response := authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-auth/register", body, credential.Secret)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("authenticated register status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/other-agent/register", body, credential.Secret)
	if response.StatusCode != http.StatusBadRequest || errorCode(t, responseBody(t, response)) != "invalid_request" {
		t.Fatalf("path mismatch status=%d", response.StatusCode)
	}

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-auth/credentials/rotate", nil, credential.Secret)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("rotate status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var replacement protocol.AgentCredentialResponse
	if err := json.NewDecoder(response.Body).Decode(&replacement); err != nil {
		t.Fatalf("decode replacement: %v", err)
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-auth/heartbeat", nil, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("old credential did not survive unactivated rotation: status=%d", response.StatusCode)
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-auth/heartbeat", nil, replacement.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replacement heartbeat status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-auth/heartbeat", nil, credential.Secret)
	if response.StatusCode != http.StatusUnauthorized || errorCode(t, responseBody(t, response)) != string(auth.CodeRevoked) {
		t.Fatalf("activated rotation left old credential valid: status=%d", response.StatusCode)
	}

	response = request(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agent-credentials/"+replacement.CredentialID+"/revoke", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("credential revoke status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-auth/heartbeat", nil, replacement.Secret)
	if response.StatusCode != http.StatusUnauthorized || errorCode(t, responseBody(t, response)) != string(auth.CodeRevoked) {
		t.Fatalf("revoked replacement status=%d", response.StatusCode)
	}
}

func TestAuthenticationErrorClassificationAndLocalManagementBoundary(t *testing.T) {
	handler := newTestHandler(t)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/heartbeat", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || errorCode(t, response.Body.Bytes()) != string(auth.CodeMissing) {
		t.Fatalf("missing credential status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/heartbeat", nil)
	request.Header.Set("Authorization", "Basic invalid")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || errorCode(t, response.Body.Bytes()) != string(auth.CodeMalformed) {
		t.Fatalf("malformed credential status=%d body=%s", response.Code, response.Body.String())
	}

	payload := []byte(`{"api_version":"setpoint.io/v1","kind":"EnrollmentToken","spec":{}}`)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/enrollment-tokens", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || errorCode(t, response.Body.Bytes()) != "local_access_required" {
		t.Fatalf("remote management status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/enrollment-tokens", bytes.NewReader(payload))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || errorCode(t, response.Body.Bytes()) != "management_proxy_rejected" {
		t.Fatalf("proxied management status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/enrollment-tokens", bytes.NewReader(payload))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("local management status=%d body=%s", response.Code, response.Body.String())
	}
}

func enrollCredential(t *testing.T, client *http.Client, baseURL, agentID string) protocol.AgentCredentialResponse {
	t.Helper()
	payload, _ := json.Marshal(protocol.CreateEnrollmentTokenRequest{
		APIVersion: "setpoint.io/v1", Kind: "EnrollmentToken",
	})
	response := request(t, client, http.MethodPost, baseURL+"/api/v1/enrollment-tokens", payload, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create enrollment token status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var enrollment protocol.EnrollmentTokenResponse
	if err := json.NewDecoder(response.Body).Decode(&enrollment); err != nil {
		t.Fatalf("decode enrollment token: %v", err)
	}
	_ = response.Body.Close()

	body, _ := json.Marshal(protocol.EnrollmentRequest{AgentID: agentID})
	response = authorizedRequest(t, client, http.MethodPost, baseURL+"/api/v1/agents/enroll", body, enrollment.Secret)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("enroll Agent status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var credential protocol.AgentCredentialResponse
	if err := json.NewDecoder(response.Body).Decode(&credential); err != nil {
		t.Fatalf("decode Agent credential: %v", err)
	}
	_ = response.Body.Close()
	return credential
}

func authorizedRequest(t *testing.T, client *http.Client, method, url string, body []byte, secret string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create authorized request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send authorized request: %v", err)
	}
	return response
}

func responseBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	var envelope json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return envelope
}
