package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementAndAgentRoutesAreIsolated(t *testing.T) {
	handlers := newTestHandlers(t)

	managementRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agents/test/heartbeat", nil)
	managementRequest.RemoteAddr = "127.0.0.1:12345"
	managementRequest.Host = "127.0.0.1:8080"
	managementResponse := httptest.NewRecorder()
	handlers.management.ServeHTTP(managementResponse, managementRequest)
	if managementResponse.Code != http.StatusNotFound {
		t.Fatalf("management listener served Agent route: status=%d body=%s", managementResponse.Code, managementResponse.Body.String())
	}

	for _, path := range []string{"/", "/healthz", "/api/v1/sites", "/api/v1/check-runs"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handlers.agent.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("Agent listener served management path %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestManagementBoundaryRejectsProxyHostAndCrossSiteBypass(t *testing.T) {
	allowed := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := ProtectManagement(allowed)

	tests := []struct {
		name       string
		remoteAddr string
		host       string
		origin     string
		fetchSite  string
		forwarded  string
		wantStatus int
		wantCode   string
	}{
		{name: "local CLI", remoteAddr: "127.0.0.1:12345", host: "127.0.0.1:8080", wantStatus: http.StatusNoContent},
		{name: "SSH tunnel browser", remoteAddr: "127.0.0.1:12345", host: "localhost:49152", origin: "http://localhost:49152", fetchSite: "same-origin", wantStatus: http.StatusNoContent},
		{name: "remote peer", remoteAddr: "192.0.2.10:12345", host: "127.0.0.1:8080", wantStatus: http.StatusForbidden, wantCode: "local_access_required"},
		{name: "DNS rebinding Host", remoteAddr: "127.0.0.1:12345", host: "attacker.example", wantStatus: http.StatusForbidden, wantCode: "management_host_rejected"},
		{name: "forwarded external request", remoteAddr: "127.0.0.1:12345", host: "127.0.0.1:8080", forwarded: "for=192.0.2.10", wantStatus: http.StatusForbidden, wantCode: "management_proxy_rejected"},
		{name: "cross-site Origin", remoteAddr: "127.0.0.1:12345", host: "127.0.0.1:8080", origin: "http://attacker.example", fetchSite: "cross-site", wantStatus: http.StatusForbidden, wantCode: "cross_site_request_rejected"},
		{name: "different loopback origin port", remoteAddr: "127.0.0.1:12345", host: "127.0.0.1:8080", origin: "http://127.0.0.1:9000", fetchSite: "same-site", wantStatus: http.StatusForbidden, wantCode: "cross_site_request_rejected"},
		{name: "cross-site fetch metadata without Origin", remoteAddr: "127.0.0.1:12345", host: "127.0.0.1:8080", fetchSite: "cross-site", wantStatus: http.StatusForbidden, wantCode: "cross_site_request_rejected"},
	}

	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/sites", nil)
			request.RemoteAddr = current.remoteAddr
			request.Host = current.host
			if current.origin != "" {
				request.Header.Set("Origin", current.origin)
			}
			if current.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", current.fetchSite)
			}
			if current.forwarded != "" {
				request.Header.Set("Forwarded", current.forwarded)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != current.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, current.wantStatus, response.Body.String())
			}
			if current.wantCode != "" && errorCode(t, response.Body.Bytes()) != current.wantCode {
				t.Fatalf("body=%s want code=%s", response.Body.String(), current.wantCode)
			}
		})
	}
}
