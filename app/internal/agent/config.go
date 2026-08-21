package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	enrollmentTokenEnvironmentVariable     = "SETPOINT_AGENT_ENROLLMENT_TOKEN"
	enrollmentTokenFileEnvironmentVariable = "SETPOINT_AGENT_ENROLLMENT_TOKEN_FILE"
)

type Config struct {
	ServerURL             string
	IdentityPath          string
	CredentialPath        string
	TaskJournalPath       string
	EnrollmentToken       string
	EnrollmentTokenFile   string
	HeartbeatInterval     time.Duration
	TaskPollInterval      time.Duration
	CommandTimeout        time.Duration
	RequestTimeout        time.Duration
	RetryMaxAttempts      int
	RetryInitialDelay     time.Duration
	RetryMaxDelay         time.Duration
	ReconnectInitialDelay time.Duration
	ReconnectMaxDelay     time.Duration
}

type fileConfig struct {
	ServerURL             string `json:"server_url"`
	IdentityPath          string `json:"identity_path"`
	CredentialPath        string `json:"credential_path"`
	TaskJournalPath       string `json:"task_journal_path"`
	EnrollmentTokenFile   string `json:"enrollment_token_file,omitempty"`
	HeartbeatInterval     string `json:"heartbeat_interval"`
	TaskPollInterval      string `json:"task_poll_interval"`
	CommandTimeout        string `json:"command_timeout"`
	RequestTimeout        string `json:"request_timeout"`
	RetryMaxAttempts      int    `json:"retry_max_attempts"`
	RetryInitialDelay     string `json:"retry_initial_delay"`
	RetryMaxDelay         string `json:"retry_max_delay"`
	ReconnectInitialDelay string `json:"reconnect_initial_delay"`
	ReconnectMaxDelay     string `json:"reconnect_max_delay"`
}

func DefaultConfig() Config {
	return Config{
		ServerURL: "http://127.0.0.1:8081", IdentityPath: "data/agent-id",
		TaskJournalPath: "data/task-journal.json", CredentialPath: "data/agent-credential.json",
		TaskPollInterval: 2 * time.Second, CommandTimeout: 10 * time.Second,
		HeartbeatInterval: 15 * time.Second, RequestTimeout: 5 * time.Second,
		RetryMaxAttempts: 5, RetryInitialDelay: 500 * time.Millisecond, RetryMaxDelay: 5 * time.Second,
		ReconnectInitialDelay: time.Second, ReconnectMaxDelay: 30 * time.Second,
	}
}

func LoadConfig(path string) (Config, error) {
	config, loadErr := loadConfig(path, os.LookupEnv)
	unsetErr := os.Unsetenv(enrollmentTokenEnvironmentVariable)
	if loadErr != nil {
		return Config{}, loadErr
	}
	if unsetErr != nil {
		return Config{}, fmt.Errorf("clear Agent enrollment token environment: %w", unsetErr)
	}
	if config.EnrollmentToken != "" && config.EnrollmentTokenFile != "" {
		return Config{}, errors.New("Agent enrollment token and enrollment token file are mutually exclusive")
	}
	return config, nil
}

func loadConfig(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	config := DefaultConfig()
	if path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read agent config: %w", err)
		}
		var fromFile fileConfig
		decoder := json.NewDecoder(strings.NewReader(string(contents)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fromFile); err != nil {
			return Config{}, fmt.Errorf("decode agent config: %w", err)
		}
		if err := applyFileConfig(&config, fromFile); err != nil {
			return Config{}, err
		}
	}
	for name, target := range map[string]*string{
		"SETPOINT_AGENT_SERVER_URL":            &config.ServerURL,
		"SETPOINT_AGENT_IDENTITY_PATH":         &config.IdentityPath,
		"SETPOINT_AGENT_TASK_JOURNAL_PATH":     &config.TaskJournalPath,
		"SETPOINT_AGENT_CREDENTIAL_PATH":       &config.CredentialPath,
		enrollmentTokenFileEnvironmentVariable: &config.EnrollmentTokenFile,
	} {
		if value, exists := lookupEnv(name); exists {
			*target = value
		}
	}
	if value, exists := lookupEnv(enrollmentTokenEnvironmentVariable); exists {
		config.EnrollmentToken = value
	}
	for name, target := range map[string]*time.Duration{
		"SETPOINT_AGENT_HEARTBEAT_INTERVAL":      &config.HeartbeatInterval,
		"SETPOINT_AGENT_TASK_POLL_INTERVAL":      &config.TaskPollInterval,
		"SETPOINT_AGENT_COMMAND_TIMEOUT":         &config.CommandTimeout,
		"SETPOINT_AGENT_REQUEST_TIMEOUT":         &config.RequestTimeout,
		"SETPOINT_AGENT_RETRY_INITIAL_DELAY":     &config.RetryInitialDelay,
		"SETPOINT_AGENT_RETRY_MAX_DELAY":         &config.RetryMaxDelay,
		"SETPOINT_AGENT_RECONNECT_INITIAL_DELAY": &config.ReconnectInitialDelay,
		"SETPOINT_AGENT_RECONNECT_MAX_DELAY":     &config.ReconnectMaxDelay,
	} {
		if value, exists := lookupEnv(name); exists {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", name, err)
			}
			*target = parsed
		}
	}
	if value, exists := lookupEnv("SETPOINT_AGENT_RETRY_MAX_ATTEMPTS"); exists {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse SETPOINT_AGENT_RETRY_MAX_ATTEMPTS: %w", err)
		}
		config.RetryMaxAttempts = parsed
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if _, err := parseServerURL(config.ServerURL); err != nil {
		return err
	}
	if strings.TrimSpace(config.IdentityPath) == "" {
		return errors.New("agent identity path is required")
	}
	if strings.TrimSpace(config.CredentialPath) == "" {
		return errors.New("agent credential path is required")
	}
	if strings.TrimSpace(config.TaskJournalPath) == "" {
		return errors.New("agent task journal path is required")
	}
	if config.EnrollmentTokenFile != "" && !filepath.IsAbs(filepath.Clean(config.EnrollmentTokenFile)) {
		return errors.New("agent enrollment token file path must be absolute")
	}
	if config.HeartbeatInterval <= 0 || config.TaskPollInterval <= 0 || config.CommandTimeout <= 0 || config.RequestTimeout <= 0 || config.RetryInitialDelay <= 0 || config.RetryMaxDelay <= 0 || config.ReconnectInitialDelay <= 0 || config.ReconnectMaxDelay <= 0 {
		return errors.New("agent durations must be positive")
	}
	if config.TaskPollInterval > time.Hour || config.CommandTimeout > 10*time.Minute {
		return errors.New("task poll interval must not exceed one hour and command timeout must not exceed ten minutes")
	}
	if config.RetryMaxAttempts < 1 {
		return errors.New("agent retry max attempts must be at least one")
	}
	if config.RetryInitialDelay > config.RetryMaxDelay {
		return errors.New("agent retry initial delay must not exceed max delay")
	}
	if config.ReconnectInitialDelay > config.ReconnectMaxDelay {
		return errors.New("agent reconnect initial delay must not exceed max delay")
	}
	return nil
}

func applyFileConfig(config *Config, fromFile fileConfig) error {
	if fromFile.ServerURL != "" {
		config.ServerURL = fromFile.ServerURL
	}
	if fromFile.IdentityPath != "" {
		config.IdentityPath = fromFile.IdentityPath
	}
	if fromFile.CredentialPath != "" {
		config.CredentialPath = fromFile.CredentialPath
	}
	if fromFile.TaskJournalPath != "" {
		config.TaskJournalPath = fromFile.TaskJournalPath
	}
	if fromFile.EnrollmentTokenFile != "" {
		config.EnrollmentTokenFile = fromFile.EnrollmentTokenFile
	}
	if fromFile.RetryMaxAttempts != 0 {
		config.RetryMaxAttempts = fromFile.RetryMaxAttempts
	}
	for name, value := range map[string]struct {
		input  string
		target *time.Duration
	}{"heartbeat_interval": {fromFile.HeartbeatInterval, &config.HeartbeatInterval}, "request_timeout": {fromFile.RequestTimeout, &config.RequestTimeout}, "retry_initial_delay": {fromFile.RetryInitialDelay, &config.RetryInitialDelay}, "retry_max_delay": {fromFile.RetryMaxDelay, &config.RetryMaxDelay}, "task_poll_interval": {fromFile.TaskPollInterval, &config.TaskPollInterval}, "command_timeout": {fromFile.CommandTimeout, &config.CommandTimeout}, "reconnect_initial_delay": {fromFile.ReconnectInitialDelay, &config.ReconnectInitialDelay}, "reconnect_max_delay": {fromFile.ReconnectMaxDelay, &config.ReconnectMaxDelay}} {
		if value.input == "" {
			continue
		}
		parsed, err := time.ParseDuration(value.input)
		if err != nil {
			return fmt.Errorf("parse agent config %s: %w", name, err)
		}
		*value.target = parsed
	}
	return nil
}

func consumeEnrollmentTokenFile(path string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || !filepath.IsAbs(clean) {
		return "", errors.New("Agent enrollment token file path must be absolute")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("inspect Agent enrollment token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Agent enrollment token file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("Agent enrollment token file permissions %04o expose secret data", info.Mode().Perm())
	}
	contents, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("read Agent enrollment token file: %w", err)
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "", errors.New("Agent enrollment token file is empty")
	}
	if err := os.Remove(clean); err != nil {
		return "", fmt.Errorf("remove consumed Agent enrollment token file: %w", err)
	}
	return token, nil
}
