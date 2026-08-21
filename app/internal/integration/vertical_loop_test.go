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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/app"
	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

func TestBackendVerticalLoopRecoversOutageAndServerRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "setpoint.db")
	store, handlers := openVerticalStack(t, databasePath)
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			_ = store.Close()
		}
	})
	managementGate := &restartableHandler{next: handlers.management}
	agentGate := &restartableHandler{next: handlers.agent}
	managementGate.setAvailable(true)
	agentGate.setAvailable(true)
	managementServer := httptest.NewServer(managementGate)
	defer managementServer.Close()
	agentServer := httptest.NewServer(agentGate)
	defer agentServer.Close()

	config := agent.DefaultConfig()
	config.ServerURL = agentServer.URL
	config.IdentityPath = filepath.Join(t.TempDir(), "agent", "agent-id")
	config.CredentialPath = filepath.Join(t.TempDir(), "agent", "credential.json")
	config.TaskJournalPath = filepath.Join(t.TempDir(), "agent", "task-journal.json")
	config.HeartbeatInterval = 10 * time.Millisecond
	config.TaskPollInterval = 5 * time.Millisecond
	config.RequestTimeout = 100 * time.Millisecond
	config.RetryMaxAttempts = 2
	config.RetryInitialDelay = 2 * time.Millisecond
	config.RetryMaxDelay = 4 * time.Millisecond
	config.ReconnectInitialDelay = 2 * time.Millisecond
	config.ReconnectMaxDelay = 8 * time.Millisecond

	enrollment := createEnrollmentToken(t, managementServer.Client(), managementServer.URL)
	config.EnrollmentToken = enrollment.Secret
	client, err := agent.NewClient(agentServer.URL, &http.Client{Timeout: config.RequestTimeout})
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-vertical-loop"
	credential, _, err := agent.BootstrapCredential(ctx, config, client, agentID)
	if err != nil {
		t.Fatalf("bootstrap Agent credential: %v", err)
	}
	registry := verticalRegistry(t)
	journal, err := agent.NewTaskJournal(config.TaskJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	commandExecutor := newBlockingExecutor()
	worker, err := agent.NewTaskWorker(
		client, agentID, "linux", registry, commandExecutor, journal, config.CommandTimeout)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := agent.NewRunner(config, client, worker, agentID, "integration",
		agent.SystemInfo{Hostname: "vertical-node", OS: "linux", OSVersion: "test", Arch: "amd64"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRunner := context.WithCancel(context.Background())
	runnerResult := make(chan error, 1)
	go func() { runnerResult <- runner.Run(runContext) }()
	runnerStopped := false
	t.Cleanup(func() {
		if !runnerStopped {
			cancelRunner()
			select {
			case <-runnerResult:
			case <-time.After(time.Second):
			}
		}
	})

	waitForCondition(t, 2*time.Second, func() bool {
		nodes := fetchNodes(t, managementServer.Client(), managementServer.URL)
		return len(nodes) == 1 && nodes[0].ID == agentID
	}, "Agent registration")
	created := createVerticalTask(t, managementServer.Client(), managementServer.URL, agentID)
	select {
	case <-commandExecutor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("formal plugin did not start")
	}

	managementGate.setAvailable(false)
	agentGate.setAvailable(false)
	close(commandExecutor.release)
	waitForCondition(t, 2*time.Second, func() bool {
		return agentGate.unavailableRegistrations.Load() > 0
	}, "Agent reconnect attempt during Server outage")
	journalContents, err := os.ReadFile(config.TaskJournalPath)
	if err != nil {
		t.Fatalf("read cached result journal: %v", err)
	}
	if strings.Contains(string(journalContents), enrollment.Secret) ||
		strings.Contains(string(journalContents), credential.Secret) {
		t.Fatal("Agent secret leaked into task journal")
	}
	managementGate.setAvailable(true)
	agentGate.setAvailable(true)

	var completed task.Resource
	waitForCondition(t, 2*time.Second, func() bool {
		completed = fetchVerticalTask(t, managementServer.Client(), managementServer.URL, created.Metadata.ID)
		return completed.Status.Phase == task.PhaseSucceeded
	}, "cached result submission after Server recovery")
	executedCommandCount := commandExecutor.calls.Load()
	if completed.Result == nil || completed.Result.State != task.CheckCompleted ||
		len(completed.Result.Items) != len(linuxicmpredirects.New().Metadata().Checks) || executedCommandCount == 0 {
		t.Fatalf("completed=%#v command_calls=%d", completed, commandExecutor.calls.Load())
	}
	waitForCondition(t, 2*time.Second, func() bool {
		_, err := os.Stat(config.TaskJournalPath)
		return os.IsNotExist(err)
	}, "Agent acknowledgement and journal cleanup")

	cancelRunner()
	select {
	case err := <-runnerResult:
		runnerStopped = true
		if err != nil {
			t.Fatalf("stop Agent runner: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Agent runner left a residual process")
	}

	managementGate.disableAndWait()
	agentGate.disableAndWait()
	if err := store.Close(); err != nil {
		t.Fatalf("close first Server store: %v", err)
	}
	storeClosed = true
	reopenedStore, reopenedHandlers := openVerticalStack(t, databasePath)
	defer reopenedStore.Close()
	managementGate.setNext(reopenedHandlers.management)
	agentGate.setNext(reopenedHandlers.agent)
	managementGate.setAvailable(true)
	agentGate.setAvailable(true)
	afterRestart := fetchVerticalTask(t, managementServer.Client(), managementServer.URL, created.Metadata.ID)
	if afterRestart.Status.Phase != task.PhaseSucceeded || afterRestart.Result == nil ||
		len(afterRestart.Result.Items) != len(linuxicmpredirects.New().Metadata().Checks) || commandExecutor.calls.Load() != executedCommandCount {
		t.Fatalf("task was not recovered after Server restart: %#v", afterRestart)
	}
	if _, err := os.Stat(config.TaskJournalPath); !os.IsNotExist(err) {
		t.Fatalf("acknowledged task journal remains: %v", err)
	}
}

func openVerticalStack(t *testing.T, databasePath string) (*storage.Store, integrationHandlers) {
	t.Helper()
	store, err := storage.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("open Server store: %v", err)
	}
	registry := verticalRegistry(t)
	service, err := app.NewService(store, store, registry, time.Second)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := service.SyncChecks(context.Background()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	return store, newIntegrationHandlers(t, store, service)
}

func verticalRegistry(t *testing.T) *plugin.CheckRegistry {
	t.Helper()
	registry := plugin.NewCheckRegistry()
	if err := registry.Register(linuxicmpredirects.New()); err != nil {
		t.Fatal(err)
	}
	return registry
}

func createVerticalTask(t *testing.T, client *http.Client, baseURL, agentID string) task.Resource {
	t.Helper()
	payload, err := json.Marshal(protocol.CreateTaskRequest{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckTask",
		Metadata: protocol.CreateTaskMetadata{IdempotencyKey: "vertical-loop-task"},
		Spec:     task.Spec{NodeID: agentID, PluginID: linuxicmpredirects.ID, Parameters: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := integrationRequest(t, client, http.MethodPost, baseURL+"/api/v1/tasks", payload, "")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create task status=%d body=%s", response.StatusCode, integrationBody(t, response))
	}
	defer response.Body.Close()
	var resource task.Resource
	if err := json.NewDecoder(response.Body).Decode(&resource); err != nil {
		t.Fatal(err)
	}
	return resource
}

func fetchVerticalTask(t *testing.T, client *http.Client, baseURL, taskID string) task.Resource {
	t.Helper()
	response := integrationRequest(t, client, http.MethodGet, baseURL+"/api/v1/tasks/"+taskID, nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get task status=%d body=%s", response.StatusCode, integrationBody(t, response))
	}
	defer response.Body.Close()
	var resource task.Resource
	if err := json.NewDecoder(response.Body).Decode(&resource); err != nil {
		t.Fatal(err)
	}
	return resource
}

type restartableHandler struct {
	mu                       sync.RWMutex
	next                     http.Handler
	available                bool
	inFlight                 sync.WaitGroup
	unavailableRegistrations atomic.Int32
}

func (handler *restartableHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	handler.mu.RLock()
	if !handler.available {
		handler.mu.RUnlock()
		if strings.HasSuffix(request.URL.Path, "/register") {
			handler.unavailableRegistrations.Add(1)
		}
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	next := handler.next
	handler.inFlight.Add(1)
	handler.mu.RUnlock()
	defer handler.inFlight.Done()
	next.ServeHTTP(writer, request)
}

func (handler *restartableHandler) setAvailable(available bool) {
	handler.mu.Lock()
	handler.available = available
	handler.mu.Unlock()
}

func (handler *restartableHandler) disableAndWait() {
	handler.setAvailable(false)
	handler.inFlight.Wait()
}

func (handler *restartableHandler) setNext(next http.Handler) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.next = next
}

type blockingExecutor struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func newBlockingExecutor() *blockingExecutor {
	return &blockingExecutor{started: make(chan struct{}), release: make(chan struct{})}
}

func (execution *blockingExecutor) Execute(ctx context.Context, _ executor.Command) (executor.Result, error) {
	execution.calls.Add(1)
	wait := false
	execution.once.Do(func() {
		close(execution.started)
		wait = true
	})
	if wait {
		select {
		case <-execution.release:
		case <-ctx.Done():
			return executor.Result{}, ctx.Err()
		}
	}
	return executor.Result{Stdout: "0\n", ExitCode: 0}, nil
}
