package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/plugins/nginxbaseline"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

func TestManagementAPIManagesSitesNodesAndDurableCheckRuns(t *testing.T) {
	handler := newTestHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	credential := enrollCredential(t, server.Client(), server.URL, "management-node")
	registerTaskAgent(t, server, credential)

	siteRequest := protocol.CreateSiteRequest{
		APIVersion: "setpoint.io/v1", Kind: "Site",
		Metadata: protocol.CreateSiteMetadata{IdempotencyKey: "site-api-idem"},
		Spec: protocol.SiteSpec{
			Name: "Internal", Description: "Read-only checks",
			TrustedExecutableRoots: []string{"/opt/company/site/bin"},
		},
	}
	siteBody, _ := json.Marshal(siteRequest)
	response := request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/sites", siteBody, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create site status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var site domain.Site
	if err := json.NewDecoder(response.Body).Decode(&site); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	siteID := site.ID
	tags := []string{"canary", "linux"}
	notes := "managed by the local Setpoint UI"
	nodeRoots := []string{"/opt/company/node/bin"}
	nodeBody, _ := json.Marshal(protocol.UpdateNodeRequest{
		SiteID: &siteID, Tags: &tags, Notes: &notes, TrustedExecutableRoots: &nodeRoots,
	})
	response = request(t, server.Client(), http.MethodPatch,
		server.URL+"/api/v1/nodes/management-node", nodeBody, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update node status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var node domain.Node
	if err := json.NewDecoder(response.Body).Decode(&node); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if node.SiteID != site.ID || node.SiteName != site.Name || len(node.Tags) != 2 ||
		len(node.TrustedExecutableRoots) != 2 {
		t.Fatalf("updated node=%#v", node)
	}

	runRequest := protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "run-api-idem", Name: "baseline"},
		Spec: protocol.CreateCheckRunSpec{
			NodeIDs: []string{credential.AgentID}, CheckIDs: []string{linuxicmpredirects.ID},
		},
	}
	runBody, _ := json.Marshal(runRequest)
	response = request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/check-runs", runBody, "application/json")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create run status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var created checkrun.Resource
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(created.Tasks) != 1 || created.Status.Counts.TotalTasks != 1 {
		t.Fatalf("created run=%#v", created)
	}
	roots := created.Tasks[0].Spec.Execution.TrustedExecutableRoots
	if len(roots) != 2 || roots[0].Scope != trustedexec.ScopeNode || roots[1].Scope != trustedexec.ScopeSite {
		t.Fatalf("frozen trusted roots=%#v", roots)
	}
	response = request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/check-runs", runBody, "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("duplicate run status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/claim", nil, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var claim protocol.ClaimTaskResponse
	if err := json.NewDecoder(response.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	ackBody, _ := json.Marshal(protocol.AcknowledgeTaskRequest{ClaimID: claim.Task.Status.ClaimID})
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/"+claim.Task.Metadata.ID+"/ack", ackBody, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ack status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	now := time.Now().UTC()
	compliant := true
	metadata := linuxicmpredirects.New().Metadata()
	items := make([]task.CheckItem, 0, len(metadata.Checks))
	for _, definition := range metadata.Checks {
		items = append(items, task.CheckItem{
			ID: definition.ID, Status: task.ItemSafe, Name: definition.Name,
			CurrentValue: "0", RecommendedValue: definition.RecommendedValue,
			Compliant: &compliant, Applicable: true, Risk: "medium",
			RiskDescription: definition.Description, Remediation: "keep disabled",
			EvidenceSummary: "bounded sysctl output", ExecutedAt: now,
		})
	}
	submission := task.ResultSubmission{
		ClaimID: claim.Task.Status.ClaimID, Phase: task.PhaseSucceeded,
		Result: &task.CheckResult{PluginID: metadata.ID, PluginVersion: metadata.Version, State: task.CheckCompleted,
			StartedAt: now, CompletedAt: now.Add(time.Millisecond), Items: items},
	}
	resultBody, _ := json.Marshal(submission)
	response = authorizedRequest(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/agents/"+credential.AgentID+"/tasks/"+claim.Task.Metadata.ID+"/result",
		resultBody, credential.Secret)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("submit result status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	_ = response.Body.Close()

	response = request(t, server.Client(), http.MethodGet,
		server.URL+"/api/v1/check-runs/"+created.Metadata.ID, nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get run status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var completed checkrun.Resource
	if err := json.NewDecoder(response.Body).Decode(&completed); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if completed.Status.Phase != checkrun.PhaseCompleted || completed.Status.Counts.Safe != len(metadata.Checks) {
		t.Fatalf("completed run=%#v", completed)
	}

	response = request(t, server.Client(), http.MethodPost,
		server.URL+"/api/v1/check-runs/"+created.Metadata.ID+"/cancel", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cancel completed run status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var canceled checkrun.CancelResponse
	if err := json.NewDecoder(response.Body).Decode(&canceled); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if canceled.Run.Status.Phase != checkrun.PhaseCompleted ||
		canceled.Report.TotalTasks != 1 ||
		canceled.Report.AlreadyTerminalTasks != 1 {
		t.Fatalf("cancel response=%#v", canceled)
	}

	response = request(t, server.Client(), http.MethodGet,
		server.URL+"/api/v1/check-runs?limit=500&offset=0", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list runs status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	var listed protocol.CheckRunListResponse
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if listed.Limit != 200 || len(listed.Runs) != 1 {
		t.Fatalf("listed runs=%#v", listed)
	}
}

func TestFreshNodeTrustedExecutableRootsJSONShape(t *testing.T) {
	handler := newTestHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	credential := enrollCredential(t, server.Client(), server.URL, "empty-roots-node")
	registerTaskAgent(t, server, credential)

	response := request(t, server.Client(), http.MethodGet,
		server.URL+"/api/v1/nodes/"+credential.AgentID, nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get node status=%d body=%s", response.StatusCode, readBody(t, response))
	}
	body := []byte(readBody(t, response))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw node JSON: %v", err)
	}
	roots, exists := raw["trusted_executable_roots"]
	if !exists {
		t.Fatalf("trusted_executable_roots missing from raw JSON: %s", body)
	}
	if string(roots) != "[]" {
		t.Fatalf("trusted_executable_roots=%s, want []", roots)
	}
	t.Logf("trusted_executable_roots raw JSON=%s", roots)
	var decoded []json.RawMessage
	if err := json.Unmarshal(roots, &decoded); err != nil {
		t.Fatalf("decode trusted_executable_roots: %v", err)
	}
	if len(decoded) != 0 {
		t.Fatalf("trusted_executable_roots length=%d, want 0", len(decoded))
	}
}

func TestCheckParametersCannotAuthorizeTrustedExecutableRoots(t *testing.T) {
	handler := newTestHandler(t)
	server := httptest.NewServer(handler)
	defer server.Close()
	credential := enrollCredential(t, server.Client(), server.URL, "trusted-root-parameter-node")
	registerTaskAgent(t, server, credential)

	payload := protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "trusted-root-parameter"},
		Spec: protocol.CreateCheckRunSpec{
			NodeIDs: []string{credential.AgentID}, CheckIDs: []string{"nginx.version"},
			Parameters: map[string]json.RawMessage{
				nginxbaseline.ID: json.RawMessage(`{"trusted_root":"/opt/unapproved/bin"}`),
			},
		},
	}
	body, _ := json.Marshal(payload)
	response := request(t, server.Client(), http.MethodPost, server.URL+"/api/v1/check-runs", body, "application/json")
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(t, response))
	}
}

func TestManagementAPIRejectsInvalidPaginationAndRemoteAccess(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/check-runs?limit=-1", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || errorCode(t, response.Body.Bytes()) != "invalid_query" {
		t.Fatalf("invalid pagination status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/sites", nil)
	request.RemoteAddr = "203.0.113.10:12345"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || errorCode(t, response.Body.Bytes()) != "local_access_required" {
		t.Fatalf("remote management status=%d body=%s", response.Code, response.Body.String())
	}
}
