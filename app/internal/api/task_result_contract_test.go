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

func TestServerRejectsResultsOutsideRegisteredCheckContract(t *testing.T) {
	metadata := linuxicmpredirects.New().Metadata()
	now := time.Now().UTC()
	makeItem := func(index int, status task.ItemStatus) task.CheckItem {
		definition := metadata.Checks[index]
		item := task.CheckItem{
			ID: definition.ID, Name: definition.Name, RecommendedValue: definition.RecommendedValue,
			Status: status, ExecutedAt: now,
		}
		switch status {
		case task.ItemSafe:
			compliant := true
			item.Applicable, item.Compliant = true, &compliant
		case task.ItemManualReview:
			item.Applicable = true
			item.ReviewReason = "the target policy is not configured"
		case task.ItemError:
			item.Applicable = true
			item.Error = &task.Failure{Code: "read_failed", Message: "read failed"}
		case task.ItemNotApplicable:
			item.Applicable = false
		}
		return item
	}
	base := func(claimID string) task.ResultSubmission {
		items := make([]task.CheckItem, 0, len(metadata.Checks))
		for index := range metadata.Checks {
			items = append(items, makeItem(index, task.ItemSafe))
		}
		return task.ResultSubmission{
			ClaimID: claimID, Phase: task.PhaseSucceeded,
			Result: &task.CheckResult{
				PluginID: metadata.ID, PluginVersion: metadata.Version, State: task.CheckCompleted,
				StartedAt: now, CompletedAt: now.Add(time.Millisecond), Items: items,
			},
		}
	}

	tests := []struct {
		name       string
		mutate     func(*task.ResultSubmission)
		wantStatus int
		replay     bool
	}{
		{name: "normal result and retransmission", wantStatus: http.StatusOK, replay: true},
		{name: "all manual review", wantStatus: http.StatusOK, mutate: func(submission *task.ResultSubmission) {
			for index := range submission.Result.Items {
				submission.Result.Items[index] = makeItem(index, task.ItemManualReview)
			}
		}},
		{name: "all not applicable", wantStatus: http.StatusOK, mutate: func(submission *task.ResultSubmission) {
			for index := range submission.Result.Items {
				submission.Result.Items[index] = makeItem(index, task.ItemNotApplicable)
			}
		}},
		{name: "partial error result", wantStatus: http.StatusOK, mutate: func(submission *task.ResultSubmission) {
			submission.Phase = task.PhaseFailed
			submission.Result.State = task.CheckError
			submission.Result.Error = &task.Failure{Code: "command_failed", Message: "command failed"}
			submission.Result.Items = []task.CheckItem{makeItem(0, task.ItemError)}
		}},
		{name: "missing item", wantStatus: http.StatusBadRequest, mutate: func(submission *task.ResultSubmission) {
			submission.Result.Items = submission.Result.Items[:len(submission.Result.Items)-1]
		}},
		{name: "unknown item", wantStatus: http.StatusBadRequest, mutate: func(submission *task.ResultSubmission) {
			unknown := makeItem(0, task.ItemSafe)
			unknown.ID = "unknown.item"
			submission.Result.Items = append(submission.Result.Items, unknown)
		}},
		{name: "duplicate item", wantStatus: http.StatusBadRequest, mutate: func(submission *task.ResultSubmission) {
			submission.Result.Items[len(submission.Result.Items)-1] = submission.Result.Items[0]
		}},
		{name: "wrong plugin ID", wantStatus: http.StatusBadRequest, mutate: func(submission *task.ResultSubmission) {
			submission.Result.PluginID = "ssh.baseline"
		}},
		{name: "old Agent plugin version", wantStatus: http.StatusBadRequest, mutate: func(submission *task.ResultSubmission) {
			submission.Result.PluginVersion = "0.9.0"
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, credential, claimed := prepareResultContractTask(t)
			submission := base(claimed.Status.ClaimID)
			if test.mutate != nil {
				test.mutate(&submission)
			}
			body, err := json.Marshal(submission)
			if err != nil {
				t.Fatal(err)
			}
			resultURL := server.URL + "/api/v1/agents/" + credential.AgentID + "/tasks/" + claimed.Metadata.ID + "/result"
			response := authorizedRequest(t, server.Client(), http.MethodPost, resultURL, body, credential.Secret)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("result status=%d body=%s", response.StatusCode, readBody(t, response))
			}
			_ = response.Body.Close()
			if test.replay {
				response = authorizedRequest(t, server.Client(), http.MethodPost, resultURL, body, credential.Secret)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("replayed result status=%d body=%s", response.StatusCode, readBody(t, response))
				}
				_ = response.Body.Close()
			}
		})
	}
}

func prepareResultContractTask(t *testing.T) (*httptest.Server, protocol.AgentCredentialResponse, task.Resource) {
	t.Helper()
	server := httptest.NewServer(newTestHandler(t))
	t.Cleanup(server.Close)
	credential := enrollCredential(t, server.Client(), server.URL, "agent-result-contract")
	registerTaskAgent(t, server, credential)
	payload, err := json.Marshal(protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckTask",
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "result-contract-task"},
		Spec:     task.Spec{NodeID: credential.AgentID, PluginID: linuxicmpredirects.ID, Parameters: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/tasks", payload, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create task status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
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
	ackBody, err := json.Marshal(protocol.AcknowledgeTaskRequest{ClaimID: claim.Task.Status.ClaimID})
	if err != nil {
		t.Fatal(err)
	}
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/"+claim.Task.Metadata.ID+"/ack", ackBody, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ack task status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()
	return server, credential, claim.Task
}
