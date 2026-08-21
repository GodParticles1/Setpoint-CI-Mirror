package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

const (
	defaultVerifyTimeout       = 45 * time.Second
	bootstrapHeartbeatInterval = 15 * time.Second
)

type Service struct {
	transports     TransportFactory
	artifacts      ArtifactProvider
	enrollment     EnrollmentAuthority
	verifier       Verifier
	advertise      string
	verifyFor      time.Duration
	heartbeatAfter time.Duration
	now            func() time.Time
}

func NewService(transports TransportFactory, artifacts ArtifactProvider, enrollment EnrollmentAuthority, verifier Verifier, advertiseURL string) (*Service, error) {
	if transports == nil || artifacts == nil || enrollment == nil || verifier == nil {
		return nil, errors.New("bootstrap transport, artifact provider, enrollment authority and verifier are required")
	}
	if err := ValidateAgentAdvertiseURL(advertiseURL); err != nil {
		return nil, err
	}
	return &Service{
		transports: transports, artifacts: artifacts, enrollment: enrollment, verifier: verifier,
		advertise: advertiseURL, verifyFor: defaultVerifyTimeout, heartbeatAfter: bootstrapHeartbeatInterval, now: time.Now,
	}, nil
}

func (service *Service) Probe(ctx context.Context, input ProbeInput) (HostProbe, error) {
	input, err := validateInput(input)
	if err != nil {
		return HostProbe{}, err
	}
	transport, err := service.transports.Connect(ctx, input, "")
	if err != nil {
		return HostProbe{}, err
	}
	defer transport.Close()
	probe, err := transport.ProbeHost(ctx, input)
	if err != nil {
		return HostProbe{}, err
	}
	if probe.HostKeyFingerprint == "" {
		return HostProbe{}, &Error{Code: ErrorHostKeyRejected, Message: "SSH host key fingerprint was not captured"}
	}
	if input.Gateway != nil && probe.GatewayHostKeyFingerprint == "" {
		return HostProbe{}, &Error{Code: ErrorHostKeyRejected, Message: "gateway SSH host key fingerprint was not captured"}
	}
	profile, err := ResolveInstallProfile(probe.OS, probe.Home, probe.UID)
	if err != nil {
		return HostProbe{}, err
	}
	probe.InstallProfile = profile
	return probe, nil
}

type ApplyInput struct {
	ProbeInput
	ExpectedHostKeyFingerprint        string
	ExpectedGatewayHostKeyFingerprint string
	SiteID                            string
}

func (service *Service) Apply(ctx context.Context, input ApplyInput) (OnlineNode, error) {
	validated, err := validateInput(input.ProbeInput)
	if err != nil {
		return OnlineNode{}, err
	}
	input.ProbeInput = validated
	input.ExpectedHostKeyFingerprint = strings.TrimSpace(input.ExpectedHostKeyFingerprint)
	input.ExpectedGatewayHostKeyFingerprint = strings.TrimSpace(input.ExpectedGatewayHostKeyFingerprint)
	if input.ExpectedHostKeyFingerprint == "" {
		return OnlineNode{}, &Error{Code: ErrorHostKeyConfirmationRequired, Message: "SSH host key confirmation is required before bootstrap"}
	}
	if !strings.HasPrefix(input.ExpectedHostKeyFingerprint, "SHA256:") {
		return OnlineNode{}, &Error{Code: ErrorHostKeyRejected, Message: "SSH host key fingerprint must use SHA256 format"}
	}
	if input.Gateway != nil {
		if input.ExpectedGatewayHostKeyFingerprint == "" {
			return OnlineNode{}, &Error{Code: ErrorHostKeyConfirmationRequired, Message: "gateway SSH host key confirmation is required before bootstrap"}
		}
		if !strings.HasPrefix(input.ExpectedGatewayHostKeyFingerprint, "SHA256:") {
			return OnlineNode{}, &Error{Code: ErrorHostKeyRejected, Message: "gateway SSH host key fingerprint must use SHA256 format"}
		}
		gateway := *input.ProbeInput.Gateway
		gateway.ExpectedHostKeyFingerprint = input.ExpectedGatewayHostKeyFingerprint
		input.ProbeInput.Gateway = &gateway
	}
	if err := ValidateRemoteAdvertiseURL(service.advertise); err != nil {
		return OnlineNode{}, err
	}

	transport, err := service.transports.Connect(ctx, input.ProbeInput, input.ExpectedHostKeyFingerprint)
	if err != nil {
		return OnlineNode{}, err
	}
	transportOpen := true
	defer func() {
		if transportOpen {
			_ = transport.Close()
		}
	}()
	probe, err := transport.ProbeHost(ctx, input.ProbeInput)
	if err != nil {
		return OnlineNode{}, err
	}
	if probe.HostKeyFingerprint != input.ExpectedHostKeyFingerprint {
		return OnlineNode{}, &Error{Code: ErrorTargetHostKeyChanged, Message: "target SSH host key changed after confirmation"}
	}
	if input.Gateway != nil && probe.GatewayHostKeyFingerprint != input.ExpectedGatewayHostKeyFingerprint {
		return OnlineNode{}, &Error{Code: ErrorGatewayHostKeyChanged, Message: "Gateway SSH host key changed after confirmation"}
	}
	if probe.AgentPresent {
		return OnlineNode{}, &Error{Code: ErrorAlreadyPresent, Message: "Setpoint Agent is already present on the target"}
	}
	profile, err := ResolveInstallProfile(probe.OS, probe.Home, probe.UID)
	if err != nil {
		return OnlineNode{}, err
	}
	artifact, err := service.artifacts.Select(ctx, probe.OS, probe.Arch)
	if err != nil {
		return OnlineNode{}, err
	}
	staging, err := stagingPath(probe.Home)
	if err != nil {
		return OnlineNode{}, err
	}
	defer transport.CleanupStaging(context.WithoutCancel(ctx), staging)
	if err := transport.PrepareStaging(ctx, staging); err != nil {
		return OnlineNode{}, err
	}

	stagedAgent := path.Join(staging, "setpoint-agent")
	stagedConfig := path.Join(staging, "config.json")
	stagedToken := path.Join(staging, "enrollment-token")
	if err := transport.UploadAgent(ctx, artifact, stagedAgent); err != nil {
		return OnlineNode{}, err
	}
	if err := transport.VerifyRemoteSHA256(ctx, stagedAgent, artifact.SHA256); err != nil {
		return OnlineNode{}, &Error{Code: ErrorArtifactHashMismatch, Message: "uploaded Agent hash does not match approved artifact", Err: err}
	}
	if err := transport.ProbeAgentRuntime(ctx, stagedAgent, service.advertise); err != nil {
		return OnlineNode{}, &Error{Code: ErrorAgentRuntimeUnreachable, Message: "target cannot reach the Setpoint Agent listener", Err: err}
	}

	token, err := service.enrollment.IssueBootstrapEnrollment(ctx)
	if err != nil {
		return OnlineNode{}, &Error{Code: ErrorEnrollmentFailed, Message: "create Agent enrollment token", Err: err}
	}
	tokenActive := true
	defer func() {
		if tokenActive {
			_ = service.enrollment.RevokeBootstrapEnrollment(context.WithoutCancel(ctx), token.ID)
		}
	}()
	configBytes, err := agentConfig(service.advertise, profile)
	if err != nil {
		return OnlineNode{}, err
	}
	if err := transport.WriteBootstrapFile(ctx, stagedConfig, configBytes, 0o600); err != nil {
		return OnlineNode{}, err
	}
	if err := transport.WriteBootstrapFile(ctx, stagedToken, []byte(token.Secret), 0o600); err != nil {
		return OnlineNode{}, err
	}

	tokenCleanupArmed := true
	defer func() {
		if tokenCleanupArmed && transportOpen {
			_ = transport.CleanupEnrollmentToken(context.WithoutCancel(ctx), profile.EnrollmentTokenPath)
		}
	}()
	if err := transport.PromoteStaging(ctx, staging, profile); err != nil {
		return OnlineNode{}, err
	}

	startedAt := service.now().UTC()
	if err := transport.StartAgent(ctx, StartSpec{AgentPath: profile.BinaryPath, ConfigPath: profile.ConfigPath}); err != nil {
		return OnlineNode{}, &Error{Code: ErrorAgentStartFailed, Message: "start Setpoint Agent", Err: err}
	}

	verifyCtx, cancel := context.WithTimeout(ctx, service.verifyFor)
	defer cancel()
	agentID, err := waitAgentID(verifyCtx, transport, profile.IdentityPath)
	if err != nil {
		return OnlineNode{}, &Error{Code: ErrorEnrollmentFailed, Message: "Agent did not create an identity after enrollment", Err: err}
	}
	node, err := service.verifier.WaitOnline(verifyCtx, agentID, startedAt)
	if err != nil {
		return OnlineNode{}, &Error{Code: ErrorHeartbeatTimeout, Message: "Agent did not become online before the verification timeout", Err: err}
	}
	if err := validateOnlineNode(node, agentID, startedAt); err != nil {
		return OnlineNode{}, err
	}

	if err := transport.Close(); err != nil {
		return OnlineNode{}, fmt.Errorf("close bootstrap SSH connection: %w", err)
	}
	transportOpen = false
	tokenCleanupArmed = false

	if err := waitDuration(ctx, service.heartbeatAfter); err != nil {
		return OnlineNode{}, &Error{Code: ErrorHeartbeatTimeout, Message: "wait for post-SSH Agent heartbeat", Err: err}
	}
	postSSHCtx, postSSHCancel := context.WithTimeout(ctx, service.verifyFor)
	defer postSSHCancel()
	postSSHNode, err := service.verifier.WaitOnline(postSSHCtx, agentID, node.LastSeenAt.Add(time.Nanosecond))
	if err != nil {
		return OnlineNode{}, &Error{Code: ErrorHeartbeatTimeout, Message: "Agent did not remain online after SSH session closed", Err: err}
	}
	if err := validateOnlineNode(postSSHNode, agentID, node.LastSeenAt.Add(time.Nanosecond)); err != nil {
		return OnlineNode{}, err
	}
	if !postSSHNode.LastSeenAt.After(node.LastSeenAt) {
		return OnlineNode{}, &Error{Code: ErrorHeartbeatTimeout, Message: "Agent heartbeat did not advance after SSH session closed"}
	}

	if strings.TrimSpace(input.SiteID) != "" {
		if err := service.verifier.AssignSite(ctx, postSSHNode.ID, strings.TrimSpace(input.SiteID)); err != nil {
			return OnlineNode{}, err
		}
	}
	if err := service.enrollment.RevokeBootstrapEnrollment(context.WithoutCancel(ctx), token.ID); err == nil {
		tokenActive = false
	}
	return postSSHNode, nil
}

func validateOnlineNode(node OnlineNode, expectedID string, notBefore time.Time) error {
	if node.ID != expectedID || node.Hostname == "" || node.OS == "" || node.Arch == "" || node.AgentVersion == "" || !node.Online || node.LastSeenAt.Before(notBefore) {
		return &Error{Code: ErrorHeartbeatTimeout, Message: "Agent registration or heartbeat evidence is incomplete"}
	}
	return nil
}

func waitDuration(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func validateInput(input ProbeInput) (ProbeInput, error) {
	input.Address = strings.TrimSpace(input.Address)
	input.Username = strings.TrimSpace(input.Username)
	if input.Port == 0 {
		input.Port = 22
	}
	if input.Address == "" || input.Username == "" || input.Password == "" {
		return ProbeInput{}, &Error{Code: ErrorInvalidRequest, Message: "address, username and password are required"}
	}
	if strings.ContainsAny(input.Address, "\r\n\x00") || strings.ContainsAny(input.Username, "\r\n\x00") {
		return ProbeInput{}, &Error{Code: ErrorInvalidRequest, Message: "bootstrap address or username contains invalid characters"}
	}
	if input.Gateway != nil {
		gateway := *input.Gateway
		gateway.Address = strings.TrimSpace(gateway.Address)
		gateway.Username = strings.TrimSpace(gateway.Username)
		if gateway.Port == 0 {
			gateway.Port = 22
		}
		if gateway.Address == "" || gateway.Username == "" || gateway.Password == "" {
			return ProbeInput{}, &Error{Code: ErrorInvalidRequest, Message: "gateway address, username and password are required"}
		}
		if strings.ContainsAny(gateway.Address, "\r\n\x00") || strings.ContainsAny(gateway.Username, "\r\n\x00") {
			return ProbeInput{}, &Error{Code: ErrorInvalidRequest, Message: "gateway address or username contains invalid characters"}
		}
		input.Gateway = &gateway
	}
	return input, nil
}

func stagingPath(home string) (string, error) {
	home = path.Clean(strings.TrimSpace(home))
	if home == "." || !path.IsAbs(home) || home == "/" {
		return "", errors.New("bootstrap requires an absolute SSH HOME")
	}
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate bootstrap staging id: %w", err)
	}
	return path.Join(home, ".setpoint-bootstrap-"+hex.EncodeToString(value[:])), nil
}

func agentConfig(serverURL string, profile InstallProfile) ([]byte, error) {
	value := struct {
		ServerURL           string `json:"server_url"`
		IdentityPath        string `json:"identity_path"`
		CredentialPath      string `json:"credential_path"`
		TaskJournalPath     string `json:"task_journal_path"`
		EnrollmentTokenFile string `json:"enrollment_token_file"`
		HeartbeatInterval   string `json:"heartbeat_interval"`
	}{
		ServerURL: serverURL, IdentityPath: profile.IdentityPath, CredentialPath: profile.CredentialPath,
		TaskJournalPath: profile.TaskJournalPath, EnrollmentTokenFile: profile.EnrollmentTokenPath,
		HeartbeatInterval: bootstrapHeartbeatInterval.String(),
	}
	return json.Marshal(value)
}

func waitAgentID(ctx context.Context, transport Transport, identityPath string) (string, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		id, err := transport.ReadAgentID(ctx, identityPath)
		if err == nil && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
