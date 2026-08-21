package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

func TestTaskCreateClaimAcknowledgeCancelResultAndQueryAPI(t *testing.T) {
	handler := newTestHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	credential := enrollCredential(t, server.Client(), server.URL, "agent-task-api")
	registerTaskAgent(t, server, credential)

	create := protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckTask",
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "task-api-idem-1"},
		Spec:     task.Spec{NodeID: "agent-task-api", PluginID: linuxicmpredirects.ID, Parameters: json.RawMessage(`{}`)},
	}
	body, _ := json.Marshal(create)
	response := request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/tasks", body, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create task status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var created task.Resource
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if created.Status.Phase != task.PhasePending || created.Metadata.ID == "" {
		t.Fatalf("created task=%#v", created)
	}
	response = request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/tasks", body, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("duplicate create status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-task-api/tasks/claim", nil, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var claim protocol.ClaimTaskResponse
	if err := json.NewDecoder(response.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if claim.Task.Metadata.ID != created.Metadata.ID || claim.Task.Status.ClaimID == "" {
		t.Fatalf("claim response=%#v", claim)
	}

	ackBody, _ := json.Marshal(protocol.AcknowledgeTaskRequest{ClaimID: claim.Task.Status.ClaimID})
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-task-api/tasks/"+created.Metadata.ID+"/ack", ackBody, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = request(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/tasks/"+created.Metadata.ID+"/cancel", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	now := time.Now().UTC()
	submission := task.ResultSubmission{
		ClaimID: claim.Task.Status.ClaimID, Phase: task.PhaseCanceled,
		Result: &task.CheckResult{
			PluginID: linuxicmpredirects.ID, PluginVersion: linuxicmpredirects.New().Metadata().Version, State: task.CheckError,
			StartedAt: now, CompletedAt: now.Add(time.Millisecond), Items: []task.CheckItem{},
			Error: &task.Failure{Code: "task_canceled", Message: "task canceled before plugin execution"},
		},
	}
	resultBody, _ := json.Marshal(submission)
	resultURL := server.URL + "/api/v1/agents/agent-task-api/tasks/" + created.Metadata.ID + "/result"
	response = authorizedRequest(t, server.Client(), http.MethodPost, resultURL, resultBody, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("submit result status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
	response = authorizedRequest(t, server.Client(), http.MethodPost, resultURL, resultBody, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("repeat result status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = request(t, server.Client(), http.MethodGet,
		server.URL+"/api/v1/tasks/"+created.Metadata.ID, nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get task status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var completed task.Resource
	if err := json.NewDecoder(response.Body).Decode(&completed); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if completed.Status.Phase != task.PhaseCanceled || completed.Result == nil || completed.Result.Error == nil {
		t.Fatalf("completed task=%#v", completed)
	}

	submission.Result.Error.Message = "different terminal result"
	conflictBody, _ := json.Marshal(submission)
	response = authorizedRequest(t, server.Client(), http.MethodPost, resultURL, conflictBody, credential.Secret)
	if response.StatusCode != http.StatusConflict || errorCode(t, responseBody(t, response)) != "task_result_conflict" {
		t.Fatalf("conflicting result status=%d", response.StatusCode)
	}

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/agent-task-api/tasks/claim", nil, credential.Secret)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("empty claim status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
}

func TestTaskAgentEndpointRequiresAuthentication(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/tasks/claim", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || errorCode(t, response.Body.Bytes()) != "auth_missing" {
		t.Fatalf("claim without authentication status=%d body=%s", response.Code, response.Body.String())
	}
}

func registerTaskAgent(t *testing.T, server *httptest.Server, credential protocol.AgentCredentialResponse) {
	t.Helper()
	registration := protocol.RegistrationRequest{
		AgentID: credential.AgentID, Hostname: "task-node", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test",
	}
	body, _ := json.Marshal(registration)
	response := authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/register", body, credential.Secret)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register task Agent status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
}
