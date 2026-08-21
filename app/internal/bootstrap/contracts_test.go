package bootstrap

import (
	"errors"
	"testing"
)

func TestResolveInstallProfileRoot(t *testing.T) {
	profile, err := ResolveInstallProfile("linux", "/root", 0)
	if err != nil {
		t.Fatalf("resolve root profile: %v", err)
	}
	if profile.Mode != "root" || profile.Root != "/opt/setpoint/agent" {
		t.Fatalf("unexpected root profile: %#v", profile)
	}
	if profile.BinaryPath != "/opt/setpoint/agent/bin/setpoint-agent" || profile.EnrollmentTokenPath != "/opt/setpoint/agent/bootstrap/enrollment-token" {
		t.Fatalf("unexpected root paths: %#v", profile)
	}
}

func TestResolveInstallProfileNonRootUsesActualHome(t *testing.T) {
	profile, err := ResolveInstallProfile("linux", "/srv/users/operator", 1039)
	if err != nil {
		t.Fatalf("resolve non-root profile: %v", err)
	}
	if profile.Mode != "non-root" || profile.Root != "/srv/users/operator/.local/share/setpoint/agent" {
		t.Fatalf("unexpected non-root profile: %#v", profile)
	}
	if profile.CredentialPath != "/srv/users/operator/.local/share/setpoint/agent/state/agent-credential.json" {
		t.Fatalf("unexpected credential path: %s", profile.CredentialPath)
	}
}

func TestResolveInstallProfileRejectsInvalidNonRootHome(t *testing.T) {
	for _, home := range []string{"", ".", "/", "relative/home"} {
		if _, err := ResolveInstallProfile("linux", home, 1000); err == nil {
			t.Fatalf("invalid home %q was accepted", home)
		}
	}
}

func TestValidateAgentAdvertiseURL(t *testing.T) {
	for _, value := range []string{"http://192.0.2.10:8081", "https://agent.example.test:8443"} {
		if err := ValidateAgentAdvertiseURL(value); err != nil {
			t.Fatalf("valid URL %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "ssh://host:22", "http://user@host:8081", "http://host:8081?q=1", "http://host:8081/#fragment"} {
		if err := ValidateAgentAdvertiseURL(value); err == nil {
			t.Fatalf("invalid URL %q accepted", value)
		}
	}
}

func TestValidateRemoteAdvertiseURLRejectsLoopback(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:8081", "http://[::1]:8081", "http://0.0.0.0:8081", "http://localhost:8081"} {
		err := ValidateRemoteAdvertiseURL(value)
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != ErrorAdvertiseURLUnavailable {
			t.Fatalf("expected typed remote advertise error for %q, got %v", value, err)
		}
	}
}
