package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesIndexAndSPAFallbackWithSecurityHeaders(t *testing.T) {
	handler := testHandler(t)
	for _, target := range []string{"/", "/runs/test-run"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="root"`) {
			t.Fatalf("target=%s code=%d body=%q", target, response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("target=%s missing security headers: %#v", target, response.Header())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("target=%s cache=%q", target, response.Header().Get("Cache-Control"))
		}
	}
}

func TestHandlerNeverFallsBackAPIOrMissingAssetsToHTML(t *testing.T) {
	handler := testHandler(t)
	for _, current := range []struct {
		target      string
		wantStatus  int
		wantContent string
	}{
		{target: "/api/v1/missing", wantStatus: http.StatusTeapot, wantContent: "application/json"},
		{target: "/api", wantStatus: http.StatusTeapot, wantContent: "application/json"},
		{target: "/healthz", wantStatus: http.StatusTeapot, wantContent: "application/json"},
		{target: "/healthz/", wantStatus: http.StatusTeapot, wantContent: "application/json"},
		{target: "/assets/missing.js", wantStatus: http.StatusNotFound, wantContent: "text/plain"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, current.target, nil))
		if response.Code != current.wantStatus || !strings.Contains(response.Header().Get("Content-Type"), current.wantContent) {
			t.Fatalf("target=%s code=%d content-type=%q body=%q", current.target, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
		if strings.Contains(response.Body.String(), `id="root"`) {
			t.Fatalf("target=%s incorrectly received SPA fallback", current.target)
		}
	}
}

func TestHandlerCachesHashedAssets(t *testing.T) {
	handler := testHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, firstEmbeddedAsset(t), nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("code=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	api := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTeapot)
		_, _ = writer.Write([]byte(`{"error":"api"}`))
	})
	handler, err := New(api)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func firstEmbeddedAsset(t *testing.T) string {
	t.Helper()
	entries, err := embedded.ReadDir("dist/assets")
	if err != nil || len(entries) == 0 {
		t.Fatalf("embedded assets: entries=%d err=%v", len(entries), err)
	}
	return "/assets/" + entries[0].Name()
}
