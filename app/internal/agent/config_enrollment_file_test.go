package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadConfigConsumesEnrollmentTokenFile(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "enrollment-token")
	const sentinel = "setpoint-enroll.password-sentinel"
	if err := os.WriteFile(tokenPath, []byte(sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "agent.json")
	contents := `{"server_url":"http://127.0.0.1:8081","identity_path":"` + filepath.ToSlash(filepath.Join(directory, "agent-id")) + `","credential_path":"` + filepath.ToSlash(filepath.Join(directory, "credential.json")) + `","task_journal_path":"` + filepath.ToSlash(filepath.Join(directory, "journal.json")) + `","enrollment_token_file":"` + filepath.ToSlash(tokenPath) + `"}`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.EnrollmentToken != "" || config.EnrollmentTokenFile == "" {
		t.Fatalf("token source was consumed during config load: %#v", config)
	}
	remote := &fakeEnrollmentClient{response: newCredentialResponse(t)}
	if _, newlyEnrolled, err := BootstrapCredential(context.Background(), config, remote, "agent-file-token"); err != nil || !newlyEnrolled || remote.enrollCalls != 1 {
		t.Fatalf("file-token enrollment failed: newly=%v calls=%d err=%v", newlyEnrolled, remote.enrollCalls, err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("consumed enrollment token file still exists: %v", err)
	}
	if strings.Contains(contents, sentinel) {
		t.Fatal("enrollment secret was embedded in Agent JSON")
	}
}

func TestLoadConfigRejectsExposedEnrollmentTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix token-file permission bits")
	}
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "enrollment-token")
	if err := os.WriteFile(tokenPath, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "agent.json")
	contents := `{"enrollment_token_file":"` + filepath.ToSlash(tokenPath) + `"}`
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load config should not consume token file: %v", err)
	}
	if _, _, err := BootstrapCredential(context.Background(), config, &fakeEnrollmentClient{}, "agent-invalid-file"); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("expected permission rejection during enrollment, got %v", err)
	}
}

func TestLoadConfigRejectsTokenEnvironmentAndFileTogether(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "enrollment-token")
	if err := os.WriteFile(tokenPath, []byte("file-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "agent.json")
	if err := os.WriteFile(configPath, []byte(`{"enrollment_token_file":"`+filepath.ToSlash(tokenPath)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(enrollmentTokenEnvironmentVariable, "environment-secret")
	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected ambiguity rejection, got %v", err)
	}
	if value := os.Getenv(enrollmentTokenEnvironmentVariable); value != "" {
		t.Fatalf("secret environment was not cleared: %q", value)
	}
}

func TestConfigRejectsRelativeEnrollmentTokenFile(t *testing.T) {
	config := DefaultConfig()
	config.EnrollmentTokenFile = "relative/token"
	if err := config.Validate(); err == nil {
		t.Fatal("relative enrollment token path was accepted")
	}
}
