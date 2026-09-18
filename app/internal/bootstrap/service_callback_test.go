package bootstrap

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestApplyAutomaticCallbackFlowsIntoRuntimeProbeAndAgentConfig(t *testing.T) {
	fixed := time.Date(2026, 9, 4, 6, 0, 0, 0, time.UTC)
	transport := &fakeTransport{probe: baseProbe(), agentID: "agent-1"}
	enrollment := &fakeEnrollment{issued: EnrollmentToken{ID: "token-1", Secret: "TOKEN_SENTINEL"}}
	first := OnlineNode{ID: "agent-1", Hostname: "node-a", OS: "linux", OSVersion: "22.03", Arch: "amd64", AgentVersion: "v1", LastSeenAt: fixed.Add(time.Second), Online: true}
	second := first
	second.LastSeenAt = first.LastSeenAt.Add(15 * time.Second)
	verifier := &fakeVerifier{nodes: []OnlineNode{first, second}}
	resolver, err := newAutomaticCallbackResolver("0.0.0.0:8081", func(_ context.Context, address string, port uint16) ([]net.IP, error) {
		if address != "192.0.2.20" || port != 22 {
			t.Fatalf("route lookup=%s:%d", address, port)
		}
		return []net.IP{net.ParseIP("192.168.50.10")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := newService(&fakeFactory{transport: transport}, fakeArtifacts{artifact: baseArtifact()}, enrollment, verifier, resolver)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return fixed }
	service.heartbeatAfter = time.Millisecond
	service.verifyFor = 50 * time.Millisecond

	if _, err := service.Apply(context.Background(), applyInput("secret")); err != nil {
		t.Fatal(err)
	}
	if !transport.runtimeProbed {
		t.Fatal("automatic callback skipped target runtime probe")
	}

	var configBytes []byte
	for name, value := range transport.written {
		if strings.HasSuffix(name, "config.json") {
			configBytes = value
			break
		}
	}
	if len(configBytes) == 0 {
		t.Fatal("Agent config was not staged")
	}
	var config struct {
		ServerURL string `json:"server_url"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	if config.ServerURL != "http://192.168.50.10:8081" {
		t.Fatalf("server_url=%q", config.ServerURL)
	}
}
