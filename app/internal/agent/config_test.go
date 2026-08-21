package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	config, err := loadConfig("", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if config.ServerURL != "http://127.0.0.1:8081" || config.RetryMaxAttempts != 5 ||
		config.ReconnectInitialDelay != time.Second || config.ReconnectMaxDelay != 30*time.Second {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadConfigFileAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	contents := []byte(`{"server_url":"http://127.0.0.1:9000","identity_path":"agent.id","heartbeat_interval":"20s","retry_max_attempts":3,"reconnect_initial_delay":"3s"}`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	environment := map[string]string{
		"SETPOINT_AGENT_SERVER_URL":          "http://127.0.0.1:9100",
		"SETPOINT_AGENT_REQUEST_TIMEOUT":     "2s",
		"SETPOINT_AGENT_RECONNECT_MAX_DELAY": "20s",
	}
	config, err := loadConfig(path, func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.ServerURL != "http://127.0.0.1:9100" || config.IdentityPath != "agent.id" ||
		config.HeartbeatInterval != 20*time.Second || config.RequestTimeout != 2*time.Second ||
		config.RetryMaxAttempts != 3 || config.ReconnectInitialDelay != 3*time.Second || config.ReconnectMaxDelay != 20*time.Second {
		t.Fatalf("unexpected merged config: %#v", config)
	}
}

func TestConfigRejectsUnboundedOrInvalidRetry(t *testing.T) {
	config := DefaultConfig()
	config.RetryMaxAttempts = 0
	if err := config.Validate(); err == nil {
		t.Fatal("zero retry limit was accepted")
	}
	config = DefaultConfig()
	config.RetryInitialDelay = 2 * time.Second
	config.RetryMaxDelay = time.Second
	if err := config.Validate(); err == nil {
		t.Fatal("retry delay above cap was accepted")
	}
	config = DefaultConfig()
	config.ReconnectInitialDelay = 2 * time.Second
	config.ReconnectMaxDelay = time.Second
	if err := config.Validate(); err == nil {
		t.Fatal("reconnect delay above cap was accepted")
	}
}
