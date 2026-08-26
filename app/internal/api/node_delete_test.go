package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/protocol"
)

func TestDeleteNodeRetiresInventoryAndRevokesOldAgentAuthority(t *testing.T) {
	server := httptest.NewServer(newTestHandler(t))
	defer server.Close()
	credential := enrollCredential(t, server.Client(), server.URL, "delete-node")
	registerTaskAgent(t, server, credential)

	response := request(t, server.Client(), http.MethodDelete, server.URL+"/api/v1/nodes/"+credential.AgentID, nil, "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete node status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	if body := readBody(t, response); body != "" {
		t.Fatalf("delete node body=%q", body)
	}
	response = request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/nodes", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list nodes status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var listed struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(listed.Nodes) != 0 {
		t.Fatalf("retired node remained listed: %s", listed.Nodes)
	}
	response = request(t, server.Client(), http.MethodGet, server.URL+"/api/v1/nodes/"+credential.AgentID, nil, "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("get retired node status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/heartbeat", nil, credential.Secret)
	if body := []byte(readBody(t, response)); response.StatusCode != http.StatusUnauthorized || errorCode(t, body) != "auth_revoked" {
		t.Fatalf("old heartbeat status=%d body=%s", response.StatusCode, body)
	}
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/claim", nil, credential.Secret)
	if body := []byte(readBody(t, response)); response.StatusCode != http.StatusUnauthorized || errorCode(t, body) != "auth_revoked" {
		t.Fatalf("old task claim status=%d body=%s", response.StatusCode, body)
	}
	registration, _ := json.Marshal(protocol.RegistrationRequest{
		AgentID: credential.AgentID, Hostname: "retired", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test",
	})
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/register", registration, credential.Secret)
	if body := []byte(readBody(t, response)); response.StatusCode != http.StatusUnauthorized || errorCode(t, body) != "auth_revoked" {
		t.Fatalf("old registration status=%d body=%s", response.StatusCode, body)
	}

	for _, id := range []string{credential.AgentID, "unknown-node"} {
		response = request(t, server.Client(), http.MethodDelete, server.URL+"/api/v1/nodes/"+id, nil, "")
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("repeat/unknown delete %q status=%d body=%s", id, response.StatusCode, readBody(t, response))
		}
		_ = response.Body.Close()
	}
}

func TestDeleteNodeBlocksPendingAndRunningWork(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "pending"
		if running {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(newTestHandler(t))
			defer server.Close()
			credential := enrollCredential(t, server.Client(), server.URL, "active-"+name+"-node")
			registerTaskAgent(t, server, credential)
			runBody, _ := json.Marshal(protocol.CreateCheckRunRequest{
				APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
				Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "active-" + name + "-run"},
				Spec:     protocol.CreateCheckRunSpec{NodeIDs: []string{credential.AgentID}, CheckIDs: []string{linuxicmpredirects.ID}},
			})
			response := request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/check-runs", runBody, "application/json")
			if response.StatusCode != http.StatusCreated {
				t.Fatalf("create check run status=%d body=%s", response.StatusCode, readBody(t, response))
			}
			_ = response.Body.Close()
			if running {
				response = authorizedRequest(t, server.Client(), http.MethodPost,
					server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/claim", nil, credential.Secret)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("claim task status=%d body=%s", response.StatusCode, readBody(t, response))
				}
				var claim protocol.ClaimTaskResponse
				if err := json.NewDecoder(response.Body).Decode(&claim); err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				ack, _ := json.Marshal(protocol.AcknowledgeTaskRequest{ClaimID: claim.Task.Status.ClaimID})
				response = authorizedRequest(t, server.Client(), http.MethodPost,
					server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/"+claim.Task.Metadata.ID+"/ack", ack, credential.Secret)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("ack task status=%d body=%s", response.StatusCode, readBody(t, response))
				}
				_ = response.Body.Close()
			}
			response = request(t, server.Client(), http.MethodDelete,
				server.URL+"/api/v1/nodes/"+credential.AgentID, nil, "")
			body := []byte(readBody(t, response))
			if response.StatusCode != http.StatusConflict || errorCode(t, body) != "active_work" {
				t.Fatalf("delete node with %s work status=%d body=%s", name, response.StatusCode, body)
			}
			response = request(t, server.Client(), http.MethodGet,
				server.URL+"/api/v1/nodes/"+credential.AgentID, nil, "")
			if response.StatusCode != http.StatusOK {
				t.Fatalf("blocked delete hid node status=%d body=%s", response.StatusCode, readBody(t, response))
			}
			_ = response.Body.Close()
		})
	}
}
