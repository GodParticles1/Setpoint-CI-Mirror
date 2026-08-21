package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/api"
	"setpoint/internal/app"
	"setpoint/internal/bootstrap"
	"setpoint/internal/plugin"
	storage "setpoint/internal/storage/sqlite"
)

const bootstrapPasswordSentinel = "SETPOINT_BOOTSTRAP_PASSWORD_SENTINEL_INTEGRATION_20260819"

type bootstrapIntegrationState struct {
	mu                      sync.Mutex
	root                    string
	agentServer             *httptest.Server
	logger                  *slog.Logger
	written                 map[string][]byte
	bootstrapConfigEvidence []byte
	enrollmentSecret        string
	agentID                 string
	closed                  int
	started                 bool
	localConfigPath         string
	localCredentialPath     string
	localTokenPath          string
	localTaskJournalPath    string
	runnerCancel            context.CancelFunc
	runnerResult            chan error
}

type bootstrapIntegrationFactory struct{ state *bootstrapIntegrationState }

func (factory bootstrapIntegrationFactory) Connect(context.Context, bootstrap.ProbeInput, string) (bootstrap.Transport, error) {
	return &bootstrapIntegrationTransport{state: factory.state}, nil
}

type bootstrapIntegrationTransport struct{ state *bootstrapIntegrationState }

func (transport *bootstrapIntegrationTransport) ProbeHost(context.Context, bootstrap.ProbeInput) (bootstrap.HostProbe, error) {
	return bootstrap.HostProbe{
		HostKeyFingerprint: "SHA256:bootstrap-integration",
		OS:                 "linux",
		OSVersion:          "24.04",
		Arch:               "amd64",
		Username:           "operator",
		UID:                1000,
		Home:               "/home/operator",
	}, nil
}

func (transport *bootstrapIntegrationTransport) PrepareStaging(context.Context, string) error {
	return nil
}
func (transport *bootstrapIntegrationTransport) UploadAgent(context.Context, bootstrap.Artifact, string) error {
	return nil
}
func (transport *bootstrapIntegrationTransport) VerifyRemoteSHA256(context.Context, string, string) error {
	return nil
}
func (transport *bootstrapIntegrationTransport) ProbeAgentRuntime(context.Context, string, string) error {
	return nil
}
func (transport *bootstrapIntegrationTransport) WriteBootstrapFile(_ context.Context, remotePath string, contents []byte, mode uint32) error {
	if mode != 0o600 {
		return fmt.Errorf("unexpected bootstrap file mode %04o", mode)
	}
	transport.state.mu.Lock()
	defer transport.state.mu.Unlock()
	if transport.state.written == nil {
		transport.state.written = map[string][]byte{}
	}
	transport.state.written[remotePath] = append([]byte(nil), contents...)
	if strings.HasSuffix(remotePath, "config.json") {
		transport.state.bootstrapConfigEvidence = append([]byte(nil), contents...)
	}
	if strings.HasSuffix(remotePath, "enrollment-token") {
		transport.state.enrollmentSecret = string(contents)
	}
	return nil
}
func (transport *bootstrapIntegrationTransport) PromoteStaging(context.Context, string, bootstrap.InstallProfile) error {
	return nil
}

func (transport *bootstrapIntegrationTransport) StartAgent(ctx context.Context, _ bootstrap.StartSpec) error {
	state := transport.state
	state.mu.Lock()
	if state.started {
		state.mu.Unlock()
		return errors.New("Agent already started by bootstrap test fixture")
	}
	state.started = true
	token := state.enrollmentSecret
	state.mu.Unlock()
	if token == "" {
		return errors.New("bootstrap enrollment token was not staged")
	}

	agentRoot := filepath.Join(state.root, "agent-runtime")
	identityPath := filepath.Join(agentRoot, "state", "agent-id")
	credentialPath := filepath.Join(agentRoot, "state", "agent-credential.json")
	taskJournalPath := filepath.Join(agentRoot, "state", "task-journal.json")
	tokenPath := filepath.Join(agentRoot, "bootstrap", "enrollment-token")
	configPath := filepath.Join(agentRoot, "config.json")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return err
	}
	info, err := os.Lstat(tokenPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		return fmt.Errorf("bootstrap token file contract violated: mode=%v", info.Mode())
	}
	configBytes, err := json.Marshal(map[string]any{
		"server_url":            state.agentServer.URL,
		"identity_path":         identityPath,
		"credential_path":       credentialPath,
		"task_journal_path":     taskJournalPath,
		"enrollment_token_file": tokenPath,
		"heartbeat_interval":    "10ms",
	})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		return err
	}
	loaded, err := agent.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load Agent token-file config: %w", err)
	}
	if _, err := os.Lstat(tokenPath); err != nil {
		return fmt.Errorf("enrollment token file disappeared during Agent LoadConfig: %v", err)
	}

	agentID := "11111111-1111-4111-8111-111111111111"
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(identityPath, []byte(agentID+"\n"), 0o600); err != nil {
		return err
	}
	client, err := agent.NewClient(state.agentServer.URL, state.agentServer.Client())
	if err != nil {
		return err
	}
	if _, _, err := agent.BootstrapCredential(ctx, loaded, client, agentID); err != nil {
		return fmt.Errorf("real Agent enrollment: %w", err)
	}
	if _, err := os.Lstat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("enrollment token file was not consumed during credential bootstrap: %v", err)
	}

	runner, err := agent.NewRunner(loaded, client, noopTaskProcessor{}, agentID, "bootstrap-integration-v1",
		agent.SystemInfo{Hostname: "bootstrap-node", OS: "linux", OSVersion: "24.04", Arch: "amd64"}, state.logger)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runner.Run(runCtx) }()

	state.mu.Lock()
	state.agentID = agentID
	state.localConfigPath = configPath
	state.localCredentialPath = credentialPath
	state.localTokenPath = tokenPath
	state.localTaskJournalPath = taskJournalPath
	state.runnerCancel = cancel
	state.runnerResult = result
	for remotePath := range state.written {
		if strings.HasSuffix(remotePath, "enrollment-token") {
			delete(state.written, remotePath)
		}
	}
	state.mu.Unlock()
	return nil
}

func (transport *bootstrapIntegrationTransport) ReadAgentID(context.Context, string) (string, error) {
	transport.state.mu.Lock()
	defer transport.state.mu.Unlock()
	if transport.state.agentID == "" {
		return "", errors.New("Agent identity not ready")
	}
	return transport.state.agentID, nil
}
func (transport *bootstrapIntegrationTransport) CleanupStaging(context.Context, string) error {
	transport.state.mu.Lock()
	defer transport.state.mu.Unlock()
	transport.state.written = map[string][]byte{}
	return nil
}
func (transport *bootstrapIntegrationTransport) CleanupEnrollmentToken(context.Context, string) error {
	transport.state.mu.Lock()
	path := transport.state.localTokenPath
	transport.state.mu.Unlock()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func (transport *bootstrapIntegrationTransport) Close() error {
	transport.state.mu.Lock()
	transport.state.closed++
	transport.state.mu.Unlock()
	return nil
}

type bootstrapIntegrationArtifacts struct{}

func (bootstrapIntegrationArtifacts) Select(context.Context, string, string) (bootstrap.Artifact, error) {
	return bootstrap.Artifact{OS: "linux", Arch: "amd64", Version: "bootstrap-integration-v1", SHA256: strings.Repeat("a", 64), Path: "/distribution/setpoint-agent-linux-amd64"}, nil
}

func TestAgentBootstrapV1RealEnrollmentSecretEvidenceAndSSHIndependence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "setpoint.db")
	store, err := storage.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	registry := plugin.NewCheckRegistry()
	service, err := app.NewService(store, store, registry, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	agentHandler, err := api.NewAgentHandler(service, logger)
	if err != nil {
		t.Fatal(err)
	}
	agentServer := httptest.NewServer(agentHandler)
	state := &bootstrapIntegrationState{root: root, agentServer: agentServer, logger: logger}
	bootstrapService, err := bootstrap.NewService(bootstrapIntegrationFactory{state: state}, bootstrapIntegrationArtifacts{}, service, service, "http://192.0.2.10:8081")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHandler, err := api.WithNodeBootstrap(http.NotFoundHandler(), bootstrapService, logger)
	if err != nil {
		t.Fatal(err)
	}
	managementServer := httptest.NewServer(bootstrapHandler)

	probeBody, err := json.Marshal(map[string]any{
		"address": "192.0.2.20", "port": 22, "username": "operator", "password": bootstrapPasswordSentinel,
	})
	if err != nil {
		t.Fatal(err)
	}
	probeResponse := postBootstrapJSON(t, managementServer.Client(), managementServer.URL+"/api/v1/node-bootstrap/probe", probeBody)
	if probeResponse.status != http.StatusOK || !bytes.Contains(probeResponse.body, []byte("SHA256:bootstrap-integration")) {
		t.Fatalf("probe status=%d body=%s", probeResponse.status, probeResponse.body)
	}
	if bytes.Contains(probeResponse.body, []byte(bootstrapPasswordSentinel)) {
		t.Fatal("probe API response leaked SSH password sentinel")
	}

	applyBody, err := json.Marshal(map[string]any{
		"address": "192.0.2.20", "port": 22, "username": "operator", "password": bootstrapPasswordSentinel,
		"expected_host_key_fingerprint": "SHA256:bootstrap-integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	applyResponse := postBootstrapJSON(t, managementServer.Client(), managementServer.URL+"/api/v1/node-bootstrap/apply", applyBody)
	if applyResponse.status != http.StatusCreated {
		t.Fatalf("apply status=%d body=%s", applyResponse.status, applyResponse.body)
	}
	var result struct {
		NodeID       string `json:"node_id"`
		Hostname     string `json:"hostname"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		AgentVersion string `json:"agent_version"`
		Status       string `json:"status"`
	}
	if err := json.Unmarshal(applyResponse.body, &result); err != nil {
		t.Fatal(err)
	}
	if result.NodeID == "" || result.Hostname == "" || result.OS == "" || result.Arch == "" || result.AgentVersion == "" || result.Status != "online" {
		t.Fatalf("bootstrap completion evidence incomplete: %#v", result)
	}

	state.mu.Lock()
	closed := state.closed
	tokenSecret := state.enrollmentSecret
	configPath := state.localConfigPath
	credentialPath := state.localCredentialPath
	tokenPath := state.localTokenPath
	taskJournalPath := state.localTaskJournalPath
	bootstrapConfig := append([]byte(nil), state.bootstrapConfigEvidence...)
	cancelRunner := state.runnerCancel
	runnerResult := state.runnerResult
	state.mu.Unlock()
	if closed < 2 {
		t.Fatalf("Probe and Apply SSH sessions were not closed: %d", closed)
	}
	if tokenSecret == "" {
		t.Fatal("integration fixture did not observe enrollment secret")
	}
	if _, err := os.Lstat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed enrollment token file still exists: %v", err)
	}

	before, err := service.GetNode(ctx, result.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	after, err := service.GetNode(ctx, result.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LastSeenAt.After(before.LastSeenAt) {
		t.Fatalf("Agent heartbeat did not continue after SSH close: before=%s after=%s", before.LastSeenAt, after.LastSeenAt)
	}

	if cancelRunner != nil {
		cancelRunner()
	}
	if runnerResult != nil {
		select {
		case err := <-runnerResult:
			if err != nil {
				t.Fatalf("Agent runner stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Agent runner did not stop during test quiesce")
		}
	}
	agentServer.Close()
	managementServer.Close()

	assertNoBootstrapSecrets(t, "bootstrap config", bootstrapConfig, bootstrapPasswordSentinel, tokenSecret)
	for _, response := range [][]byte{probeResponse.body, applyResponse.body} {
		assertNoBootstrapSecrets(t, "API response", response, bootstrapPasswordSentinel, tokenSecret)
	}
	assertNoBootstrapSecrets(t, "captured server/Agent logs", logs.Bytes(), bootstrapPasswordSentinel, tokenSecret)
	for _, evidencePath := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", configPath, credentialPath, taskJournalPath} {
		contents, err := os.ReadFile(evidencePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("read test-owned evidence %s: %v", evidencePath, err)
		}
		assertNoBootstrapSecrets(t, evidencePath, contents, bootstrapPasswordSentinel, tokenSecret)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertNoBootstrapSecrets(t, path, contents, bootstrapPasswordSentinel, tokenSecret)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type bootstrapHTTPResponse struct {
	status int
	body   []byte
}

func postBootstrapJSON(t *testing.T, client *http.Client, endpoint string, body []byte) bootstrapHTTPResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return bootstrapHTTPResponse{status: response.StatusCode, body: contents}
}

func assertNoBootstrapSecrets(t *testing.T, name string, contents []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(contents, []byte(secret)) {
			t.Fatalf("bootstrap secret persisted in %s", name)
		}
	}
}
