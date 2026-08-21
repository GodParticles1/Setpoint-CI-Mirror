package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"setpoint/internal/plugin"
	"setpoint/internal/plugins/linuxbaseline"
)

func TestManagementAPIPublishesGranularCheckCatalog(t *testing.T) {
	server := httptest.NewServer(newTestHandler(t))
	defer server.Close()

	response := request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/check-definitions", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("definitions status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var definitions struct {
		Definitions []plugin.CheckMetadata `json:"definitions"`
	}
	if err := json.NewDecoder(response.Body).Decode(&definitions); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(definitions.Definitions) < 12 || !containsDefinition(definitions.Definitions, "shell.umask") {
		t.Fatalf("definitions=%#v", definitions.Definitions)
	}

	response = request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/check-bundles", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bundles status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var bundles struct {
		Bundles []plugin.CheckBundle `json:"bundles"`
	}
	if err := json.NewDecoder(response.Body).Decode(&bundles); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !containsBundle(bundles.Bundles, linuxbaseline.ID) {
		t.Fatalf("bundles=%#v", bundles.Bundles)
	}

	response = request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/check-policies", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("policies status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
}

func containsDefinition(definitions []plugin.CheckMetadata, id string) bool {
	for _, definition := range definitions {
		if definition.ID == id {
			return true
		}
	}
	return false
}

func containsBundle(bundles []plugin.CheckBundle, id string) bool {
	for _, bundle := range bundles {
		if bundle.ID == id {
			return true
		}
	}
	return false
}
