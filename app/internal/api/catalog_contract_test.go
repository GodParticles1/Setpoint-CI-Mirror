package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckDefinitionsAPIUsesArraysForRequiredCollections(t *testing.T) {
	server := httptest.NewServer(newTestHandler(t))
	defer server.Close()

	response := request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/check-definitions", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("definitions status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	defer response.Body.Close()

	var payload struct {
		Definitions []map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Definitions) == 0 {
		t.Fatal("expected at least one check definition")
	}

	foundEmptyParameters := false
	for _, definition := range payload.Definitions {
		for _, field := range []string{"supported_systems", "parameters"} {
			raw, exists := definition[field]
			if !exists {
				t.Fatalf("definition missing required array field %q: %v", field, definition)
			}
			values := requireJSONArray(t, "definition "+field, raw)
			if field == "parameters" && len(values) == 0 {
				foundEmptyParameters = true
			}
		}
	}
	if !foundEmptyParameters {
		t.Fatal("expected a real catalog definition with empty parameters")
	}
}

func TestCheckBundlesAPIUsesArrayForCheckIDs(t *testing.T) {
	server := httptest.NewServer(newTestHandler(t))
	defer server.Close()

	response := request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/check-bundles", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bundles status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	defer response.Body.Close()

	var payload struct {
		Bundles []map[string]json.RawMessage `json:"bundles"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for _, bundle := range payload.Bundles {
		raw, exists := bundle["check_ids"]
		if !exists {
			t.Fatalf("bundle missing required check_ids array: %v", bundle)
		}
		requireJSONArray(t, "bundle check_ids", raw)
	}
}

func requireJSONArray(t *testing.T, field string, raw json.RawMessage) []json.RawMessage {
	t.Helper()
	if string(raw) == "null" {
		t.Fatalf("%s must be a JSON array, got null", field)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("%s must be a JSON array, got %s: %v", field, raw, err)
	}
	if values == nil {
		t.Fatalf("%s must be a JSON array, got %s", field, raw)
	}
	return values
}
