package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskConfigurationDefaultsAndOverrides(t *testing.T) {
	defaults := DefaultConfig()
	if defaults.TaskJournalPath != "data/task-journal.json" ||
		defaults.TaskPollInterval != 2*time.Second ||
		defaults.CommandTimeout != 10*time.Second {
		t.Fatalf("unexpected task defaults: %#v", defaults)
	}

	path := filepath.Join(t.TempDir(), "agent.json")
	contents := []byte(`{"task_journal_path":"state/tasks.json","task_poll_interval":"3s","command_timeout":"20s"}`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"SETPOINT_AGENT_TASK_JOURNAL_PATH":  "environment/tasks.json",
		"SETPOINT_AGENT_TASK_POLL_INTERVAL": "4s",
		"SETPOINT_AGENT_COMMAND_TIMEOUT":    "30s",
	}
	config, err := loadConfig(path, func(name string) (string, bool) {
		value, exists := environment[name]
		return value, exists
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TaskJournalPath != "environment/tasks.json" ||
		config.TaskPollInterval != 4*time.Second ||
		config.CommandTimeout != 30*time.Second {
		t.Fatalf("unexpected task config: %#v", config)
	}
}

func TestTaskConfigurationRejectsUnsafeBounds(t *testing.T) {
	config := DefaultConfig()
	config.TaskJournalPath = ""
	if err := config.Validate(); err == nil {
		t.Fatal("empty task journal path was accepted")
	}
	config = DefaultConfig()
	config.TaskPollInterval = time.Hour + time.Second
	if err := config.Validate(); err == nil {
		t.Fatal("unbounded task poll interval was accepted")
	}
	config = DefaultConfig()
	config.CommandTimeout = 10*time.Minute + time.Second
	if err := config.Validate(); err == nil {
		t.Fatal("unbounded command timeout was accepted")
	}
}
