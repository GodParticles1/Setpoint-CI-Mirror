package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnrollmentTokenLoadsOnlyFromEnvironment(t *testing.T) {
	config, err := loadConfig("", func(name string) (string, bool) {
		if name == "SETPOINT_AGENT_ENROLLMENT_TOKEN" {
			return "environment-only-secret", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.EnrollmentToken != "environment-only-secret" || config.CredentialPath != "data/agent-credential.json" {
		t.Fatalf("unexpected authentication config: %#v", config)
	}
}

func TestEnrollmentTokenIsRejectedInJSONConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(`{"enrollment_token":"must-not-persist"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("persisted enrollment token field was accepted")
	}
}

func TestLoadConfigClearsEnrollmentTokenEnvironment(t *testing.T) {
	t.Setenv(enrollmentTokenEnvironmentVariable, "one-use-secret")

	config, err := LoadConfig("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.EnrollmentToken != "one-use-secret" {
		t.Fatal("loaded config did not retain the enrollment token for bootstrap")
	}
	if _, exists := os.LookupEnv(enrollmentTokenEnvironmentVariable); exists {
		t.Fatal("enrollment token remains in the Agent process environment")
	}
}

func TestLoadConfigClearsEnrollmentTokenEnvironmentOnFailure(t *testing.T) {
	t.Setenv(enrollmentTokenEnvironmentVariable, "one-use-secret")

	_, err := LoadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("missing config did not fail")
	}
	if _, exists := os.LookupEnv(enrollmentTokenEnvironmentVariable); exists {
		t.Fatal("enrollment token remains after config load failure")
	}
}
