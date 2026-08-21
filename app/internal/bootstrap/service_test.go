package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeFactory struct {
	transport *fakeTransport
	expected  string
	input     ProbeInput
	connects  int
	err       error
}

func (factory *fakeFactory) Connect(_ context.Context, input ProbeInput, expected string) (Transport, error) {
	factory.connects++
	factory.input = input
	factory.expected = expected
	if factory.err != nil {
		return nil, factory.err
	}
	return factory.transport, nil
}

type fakeTransport struct {
	probe                                       HostProbe
	prepared, uploaded, runtimeProbed, promoted bool
	started, cleaned, tokenCleaned              bool
	closed                                      int
	written                                     map[string][]byte
	hashErr, runtimeErr, startErr, promoteErr   error
	writeConfigErr, writeTokenErr               error
	agentID                                     string
}

func (transport *fakeTransport) ProbeHost(context.Context, ProbeInput) (HostProbe, error) {
	return transport.probe, nil
}
func (transport *fakeTransport) PrepareStaging(context.Context, string) error {
	transport.prepared = true
	return nil
}
func (transport *fakeTransport) UploadAgent(context.Context, Artifact, string) error {
	transport.uploaded = true
	return nil
}
func (transport *fakeTransport) WriteBootstrapFile(_ context.Context, filePath string, data []byte, _ uint32) error {
	if strings.HasSuffix(filePath, "config.json") && transport.writeConfigErr != nil {
		return transport.writeConfigErr
	}
	if strings.HasSuffix(filePath, "enrollment-token") && transport.writeTokenErr != nil {
		return transport.writeTokenErr
	}
	if transport.written == nil {
		transport.written = map[string][]byte{}
	}
	transport.written[filePath] = append([]byte(nil), data...)
	return nil
}
func (transport *fakeTransport) VerifyRemoteSHA256(context.Context, string, string) error {
	return transport.hashErr
}
func (transport *fakeTransport) ProbeAgentRuntime(context.Context, string, string) error {
	transport.runtimeProbed = true
	return transport.runtimeErr
}
func (transport *fakeTransport) PromoteStaging(context.Context, string, InstallProfile) error {
	if transport.promoteErr != nil {
		return transport.promoteErr
	}
	transport.promoted = true
	return nil
}
func (transport *fakeTransport) StartAgent(context.Context, StartSpec) error {
	transport.started = true
	return transport.startErr
}
func (transport *fakeTransport) ReadAgentID(context.Context, string) (string, error) {
	if transport.agentID == "" {
		return "", errors.New("not ready")
	}
	return transport.agentID, nil
}
func (transport *fakeTransport) CleanupStaging(context.Context, string) error {
	transport.cleaned = true
	return nil
}
func (transport *fakeTransport) CleanupEnrollmentToken(context.Context, string) error {
	transport.tokenCleaned = true
	return nil
}
func (transport *fakeTransport) Close() error {
	transport.closed++
	return nil
}

type fakeArtifacts struct {
	artifact Artifact
	err      error
}

func (provider fakeArtifacts) Select(context.Context, string, string) (Artifact, error) {
	return provider.artifact, provider.err
}

type fakeEnrollment struct {
	issued     EnrollmentToken
	revoked    []string
	issueCount int
	err        error
}

func (authority *fakeEnrollment) IssueBootstrapEnrollment(context.Context) (EnrollmentToken, error) {
	authority.issueCount++
	if authority.err != nil {
		return EnrollmentToken{}, authority.err
	}
	return authority.issued, nil
}
func (authority *fakeEnrollment) RevokeBootstrapEnrollment(_ context.Context, id string) error {
	authority.revoked = append(authority.revoked, id)
	return nil
}

type fakeVerifier struct {
	nodes            []OnlineNode
	siteNode, siteID string
	err              error
	waits            []time.Time
}

func (verifier *fakeVerifier) WaitOnline(_ context.Context, _ string, after time.Time) (OnlineNode, error) {
	verifier.waits = append(verifier.waits, after)
	if verifier.err != nil {
		return OnlineNode{}, verifier.err
	}
	if len(verifier.nodes) == 0 {
		return OnlineNode{}, errors.New("no online node")
	}
	node := verifier.nodes[0]
	verifier.nodes = verifier.nodes[1:]
	return node, nil
}
func (verifier *fakeVerifier) AssignSite(_ context.Context, nodeID, siteID string) error {
	verifier.siteNode = nodeID
	verifier.siteID = siteID
	return nil
}

func baseProbe() HostProbe {
	return HostProbe{HostKeyFingerprint: "SHA256:test", OS: "linux", OSVersion: "22.03", Arch: "amd64", Username: "operator", UID: 1039, Home: "/srv/operator"}
}

func baseArtifact() Artifact {
	return Artifact{OS: "linux", Arch: "amd64", Version: "v1", SHA256: "abc", Path: "/artifact"}
}

func newApplyService(transport *fakeTransport, enrollment *fakeEnrollment, verifier *fakeVerifier, fixed time.Time) *Service {
	service, _ := NewService(&fakeFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, enrollment, verifier, "http://192.0.2.10:8081")
	service.now = func() time.Time { return fixed }
	service.heartbeatAfter = time.Millisecond
	service.verifyFor = 20 * time.Millisecond
	return service
}

func applyInput(password string) ApplyInput {
	return ApplyInput{ProbeInput: ProbeInput{Address: "192.0.2.20", Port: 22, Username: "operator", Password: password}, ExpectedHostKeyFingerprint: "SHA256:test"}
}

func TestServiceProbeResolvesNonRootProfile(t *testing.T) {
	transport := &fakeTransport{probe: baseProbe()}
	service, err := NewService(&fakeFactory{transport: transport}, fakeArtifacts{}, &fakeEnrollment{}, &fakeVerifier{}, "http://192.0.2.10:8081")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := service.Probe(context.Background(), ProbeInput{Address: "192.0.2.20", Port: 22, Username: "operator", Password: "sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	if probe.InstallProfile.Root != "/srv/operator/.local/share/setpoint/agent" {
		t.Fatalf("unexpected profile: %#v", probe.InstallProfile)
	}
	if transport.closed != 1 {
		t.Fatalf("probe SSH was not closed exactly once: %d", transport.closed)
	}
}

func TestServiceProbeRequiresGatewayFingerprintEvidence(t *testing.T) {
	probe := baseProbe()
	transport := &fakeTransport{probe: probe}
	factory := &fakeFactory{transport: transport}
	service, _ := NewService(factory, fakeArtifacts{}, &fakeEnrollment{}, &fakeVerifier{}, "http://192.0.2.10:8081")
	_, err := service.Probe(context.Background(), ProbeInput{
		Address: "10.0.0.20", Port: 22, Username: "operator", Password: "target-secret",
		Gateway: &GatewayInput{Address: "198.51.100.10", Port: 2222, Username: "jump", Password: "gateway-secret"},
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorHostKeyRejected {
		t.Fatalf("unexpected error: %v", err)
	}
	if factory.input.Gateway == nil || factory.input.Gateway.Address != "198.51.100.10" {
		t.Fatalf("gateway input not forwarded: %#v", factory.input.Gateway)
	}
	if transport.closed != 1 {
		t.Fatalf("gateway probe transport not closed: %d", transport.closed)
	}
}

func TestApplyRequiresHostKeyConfirmationBeforeConnection(t *testing.T) {
	factory := &fakeFactory{transport: &fakeTransport{probe: baseProbe()}}
	service, _ := NewService(factory, fakeArtifacts{}, &fakeEnrollment{}, &fakeVerifier{}, "http://192.0.2.10:8081")
	_, err := service.Apply(context.Background(), ApplyInput{ProbeInput: ProbeInput{Address: "192.0.2.20", Port: 22, Username: "operator", Password: "secret"}})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorHostKeyConfirmationRequired {
		t.Fatalf("unexpected error: %v", err)
	}
	if factory.connects != 0 {
		t.Fatal("connected before host-key confirmation")
	}
}

func TestApplyRequiresGatewayHostKeyConfirmationBeforeConnection(t *testing.T) {
	probe := baseProbe()
	probe.GatewayHostKeyFingerprint = "SHA256:gateway"
	factory := &fakeFactory{transport: &fakeTransport{probe: probe}}
	service, _ := NewService(factory, fakeArtifacts{}, &fakeEnrollment{}, &fakeVerifier{}, "http://192.0.2.10:8081")
	input := applyInput("target-secret")
	input.Gateway = &GatewayInput{Address: "198.51.100.10", Port: 2222, Username: "jump", Password: "gateway-secret"}
	_, err := service.Apply(context.Background(), input)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorHostKeyConfirmationRequired {
		t.Fatalf("unexpected error: %v", err)
	}
	if factory.connects != 0 {
		t.Fatal("connected before gateway host-key confirmation")
	}
}

func TestApplyForwardsConfirmedGatewayFingerprint(t *testing.T) {
	fixed := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	probe := baseProbe()
	probe.GatewayHostKeyFingerprint = "SHA256:gateway"
	transport := &fakeTransport{probe: probe, agentID: "agent-1"}
	factory := &fakeFactory{transport: transport}
	enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
	first := OnlineNode{ID: "agent-1", Hostname: "node-a", OS: "linux", OSVersion: "22.03", Arch: "amd64", AgentVersion: "v1", LastSeenAt: fixed.Add(time.Second), Online: true}
	second := first
	second.LastSeenAt = first.LastSeenAt.Add(15 * time.Second)
	verifier := &fakeVerifier{nodes: []OnlineNode{first, second}}
	service, _ := NewService(factory, fakeArtifacts{artifact: baseArtifact()}, enrollment, verifier, "http://192.0.2.10:8081")
	service.now = func() time.Time { return fixed }
	service.heartbeatAfter = time.Millisecond
	service.verifyFor = 50 * time.Millisecond

	input := applyInput("target-secret")
	input.Gateway = &GatewayInput{Address: "198.51.100.10", Port: 2222, Username: "jump", Password: "gateway-secret"}
	input.ExpectedGatewayHostKeyFingerprint = "SHA256:gateway"
	if _, err := service.Apply(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if factory.input.Gateway == nil || factory.input.Gateway.ExpectedHostKeyFingerprint != "SHA256:gateway" {
		t.Fatalf("confirmed gateway fingerprint not forwarded: %#v", factory.input.Gateway)
	}
	if factory.expected != "SHA256:test" {
		t.Fatalf("target fingerprint not forwarded: %q", factory.expected)
	}
	if !transport.runtimeProbed {
		t.Fatal("Gateway bootstrap skipped the shared runtime precheck")
	}
}

func TestApplyTargetHostKeyChangeStopsBeforeStaging(t *testing.T) {
	probe := baseProbe()
	probe.HostKeyFingerprint = "SHA256:changed-target"
	transport := &fakeTransport{probe: probe}
	enrollment := &fakeEnrollment{}
	service, _ := NewService(&fakeFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, enrollment, &fakeVerifier{}, "http://192.0.2.10:8081")
	_, err := service.Apply(context.Background(), applyInput("secret"))
	assertBootstrapErrorCode(t, err, ErrorTargetHostKeyChanged)
	if transport.prepared || transport.uploaded || enrollment.issueCount != 0 || transport.closed != 1 {
		t.Fatalf("target host-key change crossed pre-mutation gate: transport=%#v enrollment=%#v", transport, enrollment)
	}
}

func TestApplyGatewayHostKeyChangeStopsBeforeStaging(t *testing.T) {
	probe := baseProbe()
	probe.GatewayHostKeyFingerprint = "SHA256:changed-gateway"
	transport := &fakeTransport{probe: probe}
	enrollment := &fakeEnrollment{}
	service, _ := NewService(&fakeFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, enrollment, &fakeVerifier{}, "http://192.0.2.10:8081")
	input := applyInput("target-secret")
	input.Gateway = &GatewayInput{Address: "198.51.100.10", Port: 2222, Username: "jump", Password: "gateway-secret"}
	input.ExpectedGatewayHostKeyFingerprint = "SHA256:gateway"
	_, err := service.Apply(context.Background(), input)
	assertBootstrapErrorCode(t, err, ErrorGatewayHostKeyChanged)
	if transport.prepared || transport.uploaded || enrollment.issueCount != 0 || transport.closed != 1 {
		t.Fatalf("gateway host-key change crossed pre-mutation gate: transport=%#v enrollment=%#v", transport, enrollment)
	}
}

func TestApplyStopsWhenAgentAlreadyPresent(t *testing.T) {
	probe := baseProbe()
	probe.AgentPresent = true
	transport := &fakeTransport{probe: probe}
	factory := &fakeFactory{transport: transport}
	service, _ := NewService(factory, fakeArtifacts{}, &fakeEnrollment{}, &fakeVerifier{}, "http://192.0.2.10:8081")
	_, err := service.Apply(context.Background(), applyInput("secret"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorAlreadyPresent {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.prepared || transport.uploaded {
		t.Fatal("mutated target with existing Agent")
	}
	if transport.closed != 1 {
		t.Fatalf("SSH was not closed: %d", transport.closed)
	}
}

func TestApplyStagesEnrollsClosesSSHAwaitsFreshHeartbeatAndAssignsSite(t *testing.T) {
	fixed := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	transport := &fakeTransport{probe: baseProbe(), agentID: "agent-1"}
	factory := &fakeFactory{transport: transport}
	enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
	first := OnlineNode{ID: "agent-1", Hostname: "node-a", OS: "linux", OSVersion: "22.03", Arch: "amd64", AgentVersion: "v1", LastSeenAt: fixed.Add(time.Second), Online: true}
	second := first
	second.LastSeenAt = first.LastSeenAt.Add(15 * time.Second)
	verifier := &fakeVerifier{nodes: []OnlineNode{first, second}}
	service, _ := NewService(factory, fakeArtifacts{artifact: baseArtifact()}, enrollment, verifier, "http://192.0.2.10:8081")
	service.now = func() time.Time { return fixed }
	service.heartbeatAfter = time.Millisecond
	service.verifyFor = 50 * time.Millisecond

	input := applyInput("SETPOINT_BOOTSTRAP_PASSWORD_SENTINEL_UNIT")
	input.SiteID = "site-1"
	node, err := service.Apply(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if node.ID != "agent-1" || !transport.prepared || !transport.uploaded || !transport.promoted || !transport.started || !transport.cleaned {
		t.Fatalf("incomplete bootstrap: node=%#v transport=%#v", node, transport)
	}
	if !transport.runtimeProbed || enrollment.issueCount != 1 {
		t.Fatalf("runtime/token ordering evidence incomplete: transport=%#v enrollment=%#v", transport, enrollment)
	}
	if transport.closed != 1 {
		t.Fatalf("SSH must close before completion: %d", transport.closed)
	}
	if len(verifier.waits) != 2 || !verifier.waits[1].After(first.LastSeenAt) {
		t.Fatalf("post-SSH heartbeat was not required to advance: %#v", verifier.waits)
	}
	if !node.LastSeenAt.After(first.LastSeenAt) {
		t.Fatalf("returned node did not contain fresh post-SSH heartbeat: %#v", node)
	}
	if factory.expected != "SHA256:test" {
		t.Fatalf("expected fingerprint not enforced: %q", factory.expected)
	}
	if verifier.siteNode != "agent-1" || verifier.siteID != "site-1" {
		t.Fatalf("site not assigned: %#v", verifier)
	}
	if len(enrollment.revoked) == 0 || enrollment.revoked[0] != "token-1" {
		t.Fatalf("token not closed: %#v", enrollment.revoked)
	}
	for name, value := range transport.written {
		if strings.Contains(string(value), "SETPOINT_BOOTSTRAP_PASSWORD_SENTINEL_UNIT") {
			t.Fatalf("SSH password persisted in %s", name)
		}
	}
	var tokenFileFound bool
	for name, value := range transport.written {
		if strings.HasSuffix(name, "enrollment-token") && string(value) == "TOKEN_SENTINEL" {
			tokenFileFound = true
		}
	}
	if !tokenFileFound {
		t.Fatal("enrollment token file was not staged")
	}
}

func TestApplyRuntimeUnreachableStopsBeforeTokenPromotionAndStart(t *testing.T) {
	for _, mode := range []string{"direct", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			probe := baseProbe()
			input := applyInput("SSH_PASSWORD_SENTINEL")
			if mode == "gateway" {
				probe.GatewayHostKeyFingerprint = "SHA256:gateway"
				input.Gateway = &GatewayInput{Address: "198.51.100.10", Port: 2222, Username: "jump", Password: "gateway-secret"}
				input.ExpectedGatewayHostKeyFingerprint = "SHA256:gateway"
			}
			transport := &fakeTransport{probe: probe, runtimeErr: errors.New("runtime endpoint blocked")}
			enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
			service, _ := NewService(&fakeFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, enrollment, &fakeVerifier{}, "http://192.0.2.10:8081")
			_, err := service.Apply(context.Background(), input)
			assertBootstrapErrorCode(t, err, ErrorAgentRuntimeUnreachable)
			if !transport.prepared || !transport.uploaded || !transport.runtimeProbed {
				t.Fatalf("runtime precheck did not run after checked upload: %#v", transport)
			}
			if enrollment.issueCount != 0 || transport.promoted || transport.started || transport.tokenCleaned {
				t.Fatalf("runtime failure crossed pre-mutation gate: transport=%#v enrollment=%#v", transport, enrollment)
			}
			if !transport.cleaned || transport.closed != 1 {
				t.Fatalf("runtime failure cleanup incomplete: %#v", transport)
			}
		})
	}
}

func TestApplyHashMismatchStopsBeforePromotionAndCleansStaging(t *testing.T) {
	transport := &fakeTransport{probe: baseProbe(), hashErr: errors.New("mismatch")}
	service, _ := NewService(&fakeFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, &fakeEnrollment{issued: EnrollmentToken{ID: "t", Secret: "s"}}, &fakeVerifier{}, "http://192.0.2.10:8081")
	_, err := service.Apply(context.Background(), applyInput("secret"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorArtifactHashMismatch {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.promoted || transport.started {
		t.Fatal("continued after artifact hash mismatch")
	}
	if !transport.cleaned || transport.tokenCleaned {
		t.Fatal("pre-promotion failure must clean only run-owned staging")
	}
	if transport.closed != 1 {
		t.Fatalf("SSH was not closed: %d", transport.closed)
	}
}

func TestApplyStartFailureCleansOnlyPromotedBootstrapTokenAndStaging(t *testing.T) {
	fixed := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	transport := &fakeTransport{probe: baseProbe(), agentID: "agent-1", startErr: errors.New("start failed")}
	enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
	service := newApplyService(transport, enrollment, &fakeVerifier{}, fixed)
	_, err := service.Apply(context.Background(), applyInput("secret"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorAgentStartFailed {
		t.Fatalf("unexpected error: %v", err)
	}
	if !transport.promoted || !transport.cleaned || !transport.tokenCleaned {
		t.Fatalf("failed bootstrap cleanup incomplete: %#v", transport)
	}
	if transport.closed != 1 {
		t.Fatalf("SSH was not closed: %d", transport.closed)
	}
}

func TestApplyHeartbeatFailureCleansTokenClosesSSHAndNeverReportsSuccess(t *testing.T) {
	fixed := time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC)
	transport := &fakeTransport{probe: baseProbe(), agentID: "agent-1"}
	enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
	service := newApplyService(transport, enrollment, &fakeVerifier{err: errors.New("offline")}, fixed)
	_, err := service.Apply(context.Background(), applyInput("secret"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorHeartbeatTimeout {
		t.Fatalf("unexpected error: %v", err)
	}
	if !transport.cleaned || !transport.tokenCleaned {
		t.Fatalf("failed bootstrap cleanup incomplete: %#v", transport)
	}
	if transport.closed != 1 {
		t.Fatalf("SSH was not closed: %d", transport.closed)
	}
}
