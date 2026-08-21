package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type cleanupMatrixFactory struct{ transport Transport }

func (factory cleanupMatrixFactory) Connect(context.Context, ProbeInput, string) (Transport, error) {
	return factory.transport, nil
}

type cleanupMatrixTransport struct {
	*fakeTransport
	failAt string
}

func (transport *cleanupMatrixTransport) PrepareStaging(ctx context.Context, staging string) error {
	transport.prepared = true
	if transport.failAt == "prepare" {
		return errors.New("prepare failed after partial staging creation")
	}
	return nil
}
func (transport *cleanupMatrixTransport) UploadAgent(ctx context.Context, artifact Artifact, target string) error {
	if transport.failAt == "upload" {
		return errors.New("upload failed")
	}
	return transport.fakeTransport.UploadAgent(ctx, artifact, target)
}
func (transport *cleanupMatrixTransport) VerifyRemoteSHA256(ctx context.Context, target, expected string) error {
	if transport.failAt == "hash" {
		return errors.New("hash mismatch")
	}
	return transport.fakeTransport.VerifyRemoteSHA256(ctx, target, expected)
}
func (transport *cleanupMatrixTransport) ProbeAgentRuntime(ctx context.Context, agentPath, serverURL string) error {
	transport.runtimeProbed = true
	if transport.failAt == "runtime" {
		return errors.New("runtime unavailable")
	}
	return transport.fakeTransport.ProbeAgentRuntime(ctx, agentPath, serverURL)
}
func (transport *cleanupMatrixTransport) WriteBootstrapFile(ctx context.Context, target string, data []byte, mode uint32) error {
	if transport.failAt == "config" && strings.HasSuffix(target, "config.json") {
		return errors.New("config write failed")
	}
	if transport.failAt == "token" && strings.HasSuffix(target, "enrollment-token") {
		return errors.New("token write failed")
	}
	return transport.fakeTransport.WriteBootstrapFile(ctx, target, data, mode)
}
func (transport *cleanupMatrixTransport) PromoteStaging(ctx context.Context, staging string, profile InstallProfile) error {
	if transport.failAt == "promote" {
		return errors.New("promote failed after possible partial move")
	}
	return transport.fakeTransport.PromoteStaging(ctx, staging, profile)
}

func TestApplyFailureCleanupMatrix(t *testing.T) {
	cases := []struct {
		name             string
		failAt           string
		enrollmentErr    error
		wantTokenCleanup bool
	}{
		{name: "prepare-after-partial-create", failAt: "prepare"},
		{name: "enrollment-issue", enrollmentErr: errors.New("issue failed")},
		{name: "upload", failAt: "upload"},
		{name: "after-upload-before-hash", failAt: "hash"},
		{name: "runtime-precheck", failAt: "runtime"},
		{name: "config-write", failAt: "config"},
		{name: "token-write", failAt: "token"},
		{name: "promote-after-token-write", failAt: "promote", wantTokenCleanup: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			base := &fakeTransport{probe: baseProbe(), agentID: "agent-1"}
			transport := &cleanupMatrixTransport{fakeTransport: base, failAt: testCase.failAt}
			enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}, err: testCase.enrollmentErr}
			service, err := NewService(cleanupMatrixFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, enrollment, &fakeVerifier{}, "http://192.0.2.10:8081")
			if err != nil {
				t.Fatal(err)
			}
			service.verifyFor = 20 * time.Millisecond
			service.heartbeatAfter = time.Millisecond
			if _, err := service.Apply(context.Background(), applyInput("SETPOINT_BOOTSTRAP_PASSWORD_SENTINEL_CLEANUP")); err == nil {
				t.Fatal("expected bootstrap failure")
			}
			if !base.cleaned {
				t.Fatal("run-owned staging was not cleaned")
			}
			if base.closed != 1 {
				t.Fatalf("SSH close count = %d, want 1", base.closed)
			}
			if base.tokenCleaned != testCase.wantTokenCleanup {
				t.Fatalf("final token cleanup = %v, want %v", base.tokenCleaned, testCase.wantTokenCleanup)
			}
			if base.started {
				t.Fatal("Agent was started after an earlier phase failure")
			}
		})
	}
}

func TestApplyEnrollmentIdentityTimeoutCleansTokenAndClosesSSH(t *testing.T) {
	base := &fakeTransport{probe: baseProbe()}
	enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
	service, err := NewService(cleanupMatrixFactory{transport: base}, fakeArtifacts{artifact: baseArtifact()}, enrollment, &fakeVerifier{}, "http://192.0.2.10:8081")
	if err != nil {
		t.Fatal(err)
	}
	service.verifyFor = 5 * time.Millisecond
	service.heartbeatAfter = time.Millisecond
	_, err = service.Apply(context.Background(), applyInput("secret"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorEnrollmentFailed {
		t.Fatalf("unexpected error: %v", err)
	}
	if !base.cleaned || !base.tokenCleaned || base.closed != 1 {
		t.Fatalf("enrollment-timeout cleanup incomplete: %#v", base)
	}
}
