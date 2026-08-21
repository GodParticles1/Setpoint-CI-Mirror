package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"setpoint/internal/protocol"
)

func TestProbeRuntimeAcceptsExactAgentListenerContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != protocol.AgentRuntimeReadyPath {
			t.Fatalf("unexpected probe request: %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","service":"setpoint-agent-listener","contract_version":"v1"}`))
	}))
	defer server.Close()
	if err := ProbeRuntime(context.Background(), server.URL, server.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRuntimeFailsClosedOnWrongContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","service":"management","contract_version":"v1"}`))
	}))
	defer server.Close()
	if err := ProbeRuntime(context.Background(), server.URL, server.Client()); err == nil {
		t.Fatal("wrong runtime marker was accepted")
	}
}

func TestProbeRuntimeDoesNotFollowRedirects(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"status":"ok","service":"setpoint-agent-listener","contract_version":"v1"}`))
	}))
	defer destination.Close()
	source := httptest.NewServer(http.RedirectHandler(destination.URL, http.StatusFound))
	defer source.Close()
	if err := ProbeRuntime(context.Background(), source.URL, source.Client()); err == nil {
		t.Fatal("redirected runtime endpoint was accepted")
	}
}

func TestProbeRuntimeRejectsCredentialBearingServerURL(t *testing.T) {
	err := ProbeRuntime(context.Background(), "http://user:SECRET_SENTINEL@example.invalid", http.DefaultClient)
	if err == nil {
		t.Fatal("credential-bearing server URL was accepted")
	}
	if strings.Contains(err.Error(), "SECRET_SENTINEL") {
		t.Fatal("credential-bearing server URL leaked through validation error")
	}
}

func TestProbeRuntimeRejectsInvalidResponses(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "non-200", status: http.StatusServiceUnavailable, contentType: "application/json", body: `{}`},
		{name: "non-json", status: http.StatusOK, contentType: "text/plain", body: `ok`},
		{name: "malformed-json", status: http.StatusOK, contentType: "application/json", body: `{`},
		{name: "unknown-field", status: http.StatusOK, contentType: "application/json", body: `{"status":"ok","service":"setpoint-agent-listener","contract_version":"v1","extra":true}`},
		{name: "multiple-values", status: http.StatusOK, contentType: "application/json", body: `{"status":"ok","service":"setpoint-agent-listener","contract_version":"v1"} {}`},
		{name: "oversized", status: http.StatusOK, contentType: "application/json", body: strings.Repeat("x", maxRuntimeProbeResponse+1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", testCase.contentType)
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			if err := ProbeRuntime(context.Background(), server.URL, server.Client()); err == nil {
				t.Fatalf("invalid runtime response was accepted")
			}
		})
	}
}
