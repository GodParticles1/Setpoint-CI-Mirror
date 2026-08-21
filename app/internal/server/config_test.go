package server

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
	if config.ManagementListenAddress != defaultManagementListenAddress ||
		config.AgentListenAddress != defaultAgentListenAddress ||
		config.AgentAdvertiseURL != defaultAgentAdvertiseURL ||
		config.DatabasePath != defaultDatabasePath || config.MaxHeaderBytes != defaultMaxHeaderBytes {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadConfigFileAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	contents := []byte(`{"management_listen_address":"127.0.0.1:9000","agent_listen_address":"192.0.2.10:9001","agent_advertise_url":"http://192.0.2.10:9001","database_path":"file.db","offline_after":"30s","max_header_bytes":524288}`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	environment := map[string]string{
		"SETPOINT_SERVER_MANAGEMENT_LISTEN":   "127.0.0.1:9100",
		"SETPOINT_SERVER_AGENT_LISTEN":        "192.0.2.11:9101",
		"SETPOINT_SERVER_AGENT_ADVERTISE_URL": "http://192.0.2.11:9101",
		"SETPOINT_SERVER_SHUTDOWN_TIMEOUT":    "2s",
		"SETPOINT_SERVER_MAX_HEADER_BYTES":    "262144",
	}
	config, err := loadConfig(path, func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.ManagementListenAddress != "127.0.0.1:9100" || config.AgentListenAddress != "192.0.2.11:9101" ||
		config.AgentAdvertiseURL != "http://192.0.2.11:9101" || config.DatabasePath != "file.db" ||
		config.OfflineAfter != 30*time.Second || config.ShutdownTimeout != 2*time.Second || config.MaxHeaderBytes != 262144 {
		t.Fatalf("unexpected merged config: %#v", config)
	}
}

func TestConfigRejectsNonLoopbackManagementListeners(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8080", "192.0.2.10:8080", ":8080", "localhost:8080"} {
		t.Run(address, func(t *testing.T) {
			config := DefaultConfig()
			config.ManagementListenAddress = address
			if err := config.Validate(); err == nil {
				t.Fatalf("non-literal-loopback management address %q was accepted", address)
			}
		})
	}
}

func TestConfigAllowsExplicitAgentLANListenerAndAdvertiseURL(t *testing.T) {
	config := DefaultConfig()
	config.ManagementListenAddress = "[::1]:8080"
	config.AgentListenAddress = "0.0.0.0:8081"
	config.AgentAdvertiseURL = "http://192.0.2.10:8081"
	if err := config.Validate(); err != nil {
		t.Fatalf("validate split listeners: %v", err)
	}
}

func TestConfigRejectsInvalidAgentAdvertiseURL(t *testing.T) {
	for _, value := range []string{"", "ssh://host:22", "http://user@host:8081", "http://host:8081?q=1", "http://host:8081/#fragment"} {
		t.Run(value, func(t *testing.T) {
			config := DefaultConfig()
			config.AgentAdvertiseURL = value
			if err := config.Validate(); err == nil {
				t.Fatalf("invalid Agent advertise URL %q was accepted", value)
			}
		})
	}
}

func TestConfigRejectsIdenticalListeners(t *testing.T) {
	config := DefaultConfig()
	config.AgentListenAddress = config.ManagementListenAddress
	if err := config.Validate(); err == nil {
		t.Fatal("identical management and Agent listeners were accepted")
	}
}

func TestConfigRejectsInvalidMaxHeaderBytes(t *testing.T) {
	config := DefaultConfig()
	config.MaxHeaderBytes = 0
	if err := config.Validate(); err == nil {
		t.Fatal("zero max header bytes was accepted")
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := loadConfig(path, func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("unknown config field was accepted")
	}
}
