package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/app"
	"setpoint/internal/checkrun"
	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
	"setpoint/internal/webui"
)

func TestRC1RealComponentsSurviveServerRestartWithoutTaskReplay(t *testing.T) {
	ctx := context.Background()
	tempRoot := t.TempDir()
	databasePath := filepath.Join(tempRoot, "server", "setpoint.db")
	store, handlers := openRC1Stack(t, databasePath)
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			_ = store.Close()
		}
	})

	managementGate := &restartableHandler{next: handlers.management, available: true}
	agentGate := &restartableHandler{next: handlers.agent, available: true}
	managementServer := httptest.NewServer(managementGate)
	agentServer := httptest.NewServer(agentGate)
	t.Cleanup(managementServer.Close)
	t.Cleanup(agentServer.Close)
	assertRC1ManagementSurface(t, managementServer)

	config := agent.DefaultConfig()
	config.ServerURL = agentServer.URL
	config.IdentityPath = filepath.Join(tempRoot, "agent", "identity")
	config.CredentialPath = filepath.Join(tempRoot, "agent", "credential.json")
	config.TaskJournalPath = filepath.Join(tempRoot, "agent", "task-journal.json")
	config.HeartbeatInterval = 10 * time.Millisecond
	config.TaskPollInterval = 5 * time.Millisecond
	config.RequestTimeout = 200 * time.Millisecond
	config.CommandTimeout = 5 * time.Second
	config.RetryMaxAttempts = 2
	config.RetryInitialDelay = 2 * time.Millisecond
	config.RetryMaxDelay = 4 * time.Millisecond
	config.ReconnectInitialDelay = 2 * time.Millisecond
	config.ReconnectMaxDelay = 8 * time.Millisecond

	enrollment := createEnrollmentToken(t, managementServer.Client(), managementServer.URL)
	config.EnrollmentToken = enrollment.Secret
	client, err := agent.NewClient(agentServer.URL, agentServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-rc1-smoke"
	credential, _, err := agent.BootstrapCredential(ctx, config, client, agentID)
	if err != nil {
		t.Fatalf("bootstrap Agent credential: %v", err)
	}
	config.EnrollmentToken = ""
	reusedClient, err := agent.NewClient(agentServer.URL, agentServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	reused, _, err := agent.BootstrapCredential(ctx, config, reusedClient, agentID)
	if err != nil || reused.CredentialID != credential.CredentialID || reused.Secret != credential.Secret {
		t.Fatalf("reuse persisted Agent credential: same=%t err=%v", reused.CredentialID == credential.CredentialID && reused.Secret == credential.Secret, err)
	}
	credentialContents, err := os.ReadFile(config.CredentialPath)
	if err != nil {
		t.Fatalf("read persisted credential: %v", err)
	}
	if strings.Contains(string(credentialContents), enrollment.Secret) {
		t.Fatal("Enrollment Token leaked into persisted Agent credential")
	}

	registry := rc1FormalRegistry(t)
	commandExecutor, err := executor.NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := agent.NewTaskJournal(config.TaskJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := agent.NewTaskWorker(reusedClient, agentID, runtime.GOOS, registry, commandExecutor, journal, config.CommandTimeout)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := agent.NewRunner(config, reusedClient, worker, agentID, "rc1-smoke",
		agent.SystemInfo{Hostname: "rc1-local", OS: runtime.GOOS, OSVersion: "local", Arch: runtime.GOARCH}, logger)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRunner := context.WithCancel(context.Background())
	runnerResult := make(chan error, 1)
	go func() { runnerResult <- runner.Run(runContext) }()
	runnerStopped := false
	t.Cleanup(func() {
		if runnerStopped {
			return
		}
		cancelRunner()
		select {
		case <-runnerResult:
		case <-time.After(2 * time.Second):
		}
	})

	waitForCondition(t, 3*time.Second, func() bool {
		nodes := fetchNodes(t, managementServer.Client(), managementServer.URL)
		return len(nodes) == 1 && nodes[0].ID == agentID
	}, "RC1 Agent online")
	created := createRC1CheckRun(t, managementServer, agentID)
	taskID := created.Tasks[0].Metadata.ID
	var completed task.Resource
	waitForCondition(t, 5*time.Second, func() bool {
		completed = fetchVerticalTask(t, managementServer.Client(), managementServer.URL, taskID)
		return completed.Status.Phase == task.PhaseSucceeded
	}, "RC1 read-only task completion")
	if completed.Status.Attempt != 1 || completed.Result == nil || completed.Result.State != task.CheckCompleted ||
		len(completed.Result.Items) != 1 || completed.Result.Items[0].ID != "login.motd" {
		t.Fatalf("unexpected RC1 task result: %#v", completed)
	}
	waitForCondition(t, 2*time.Second, func() bool {
		_, err := os.Stat(config.TaskJournalPath)
		return os.IsNotExist(err)
	}, "RC1 journal acknowledgement cleanup")

	managementGate.disableAndWait()
	agentGate.disableAndWait()
	waitForCondition(t, 2*time.Second, func() bool {
		return agentGate.unavailableRegistrations.Load() > 0
	}, "RC1 Agent reconnect during Server outage")
	if err := store.Close(); err != nil {
		t.Fatalf("close RC1 Server store: %v", err)
	}
	storeClosed = true
	reopenedStore, reopenedHandlers := openRC1Stack(t, databasePath)
	reopenedClosed := false
	t.Cleanup(func() {
		if !reopenedClosed {
			_ = reopenedStore.Close()
		}
	})
	managementGate.setNext(reopenedHandlers.management)
	agentGate.setNext(reopenedHandlers.agent)
	managementGate.setAvailable(true)
	agentGate.setAvailable(true)
	assertRC1ManagementSurface(t, managementServer)
	waitForCondition(t, 3*time.Second, func() bool {
		nodes := fetchNodes(t, managementServer.Client(), managementServer.URL)
		return len(nodes) == 1 && nodes[0].ID == agentID
	}, "RC1 Agent reconnect after Server restart")
	afterRestart := fetchVerticalTask(t, managementServer.Client(), managementServer.URL, taskID)
	if afterRestart.Status.Phase != task.PhaseSucceeded || afterRestart.Status.Attempt != 1 ||
		afterRestart.Result == nil || len(afterRestart.Result.Items) != 1 {
		t.Fatalf("RC1 task changed or replayed after Server restart: %#v", afterRestart)
	}

	cancelRunner()
	select {
	case err := <-runnerResult:
		runnerStopped = true
		if err != nil {
			t.Fatalf("stop RC1 Agent runner: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RC1 Agent runner left a residual goroutine")
	}
	managementGate.disableAndWait()
	agentGate.disableAndWait()
	agentServer.Close()
	managementServer.Close()
	if err := reopenedStore.Close(); err != nil {
		t.Fatalf("close restarted RC1 store: %v", err)
	}
	reopenedClosed = true
	storeClosed = true
	if _, err := os.Stat(config.TaskJournalPath); !os.IsNotExist(err) {
		t.Fatalf("RC1 task journal remains after cleanup: %v", err)
	}
	runtime.GC()
	removeRC1TempRoot(t, tempRoot)
}

func openRC1Stack(t *testing.T, databasePath string) (*storage.Store, integrationHandlers) {
	t.Helper()
	store, err := storage.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("open RC1 Server store: %v", err)
	}
	registry := rc1FormalRegistry(t)
	service, err := app.NewService(store, store, registry, time.Second)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := service.SyncChecks(context.Background()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	handlers := newIntegrationHandlers(t, store, service)
	management, err := webui.New(handlers.management)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	handlers.management = management
	return store, handlers
}

func rc1FormalRegistry(t *testing.T) *plugin.CheckRegistry {
	t.Helper()
	registry := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	return registry
}

func createRC1CheckRun(t *testing.T, server *httptest.Server, agentID string) checkrun.Resource {
	t.Helper()
	payload, err := json.Marshal(protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "rc1-real-component-smoke", Name: "RC1 local smoke"},
		Spec:     protocol.CreateCheckRunSpec{NodeIDs: []string{agentID}, CheckIDs: []string{"login.motd"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := integrationRequest(t, server.Client(), http.MethodPost, server.URL+"/api/v1/check-runs", payload, "")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create RC1 check run status=%d body=%s", response.StatusCode, integrationBody(t, response))
	}
	defer response.Body.Close()
	var resource checkrun.Resource
	if err := json.NewDecoder(response.Body).Decode(&resource); err != nil {
		t.Fatal(err)
	}
	if len(resource.Tasks) != 1 {
		t.Fatalf("RC1 check run tasks=%d, want 1", len(resource.Tasks))
	}
	return resource
}

func assertRC1ManagementSurface(t *testing.T, server *httptest.Server) {
	t.Helper()
	for _, target := range []struct {
		path, contains string
	}{{"/healthz", `"status":"ok"`}, {"/", `id="root"`}} {
		response, err := server.Client().Get(server.URL + target.path)
		if err != nil {
			t.Fatalf("get RC1 management %s: %v", target.path, err)
		}
		body := integrationBody(t, response)
		if response.StatusCode != http.StatusOK || !strings.Contains(body, target.contains) {
			t.Fatalf("RC1 management %s status=%d body=%q", target.path, response.StatusCode, body)
		}
	}
}

func removeRC1TempRoot(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := os.RemoveAll(root)
		_, statErr := os.Stat(root)
		if err == nil && os.IsNotExist(statErr) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("remove RC1 temporary root: remove=%v stat=%v", err, statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
