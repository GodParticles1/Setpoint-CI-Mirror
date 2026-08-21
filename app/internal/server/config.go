package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/bootstrap"
)

const (
	defaultManagementListenAddress = "127.0.0.1:8080"
	defaultAgentListenAddress      = "127.0.0.1:8081"
	defaultAgentAdvertiseURL       = "http://127.0.0.1:8081"
	defaultAgentArtifactsDirectory = "agents"
	defaultDatabasePath            = "data/setpoint.db"
	defaultMaxHeaderBytes          = 1 << 20
)

type Config struct {
	ManagementListenAddress string
	AgentListenAddress      string
	AgentAdvertiseURL       string
	AgentArtifactsDirectory string
	DatabasePath            string
	OfflineAfter            time.Duration
	ShutdownTimeout         time.Duration
	ReadHeaderTimeout       time.Duration
	IdleTimeout             time.Duration
	MaxHeaderBytes          int
}

type fileConfig struct {
	ManagementListenAddress string `json:"management_listen_address"`
	AgentListenAddress      string `json:"agent_listen_address"`
	AgentAdvertiseURL       string `json:"agent_advertise_url"`
	AgentArtifactsDirectory string `json:"agent_artifacts_directory"`
	DatabasePath            string `json:"database_path"`
	OfflineAfter            string `json:"offline_after"`
	ShutdownTimeout         string `json:"shutdown_timeout"`
	ReadHeaderTimeout       string `json:"read_header_timeout"`
	IdleTimeout             string `json:"idle_timeout"`
	MaxHeaderBytes          int    `json:"max_header_bytes"`
}

func DefaultConfig() Config {
	return Config{
		ManagementListenAddress: defaultManagementListenAddress,
		AgentListenAddress:      defaultAgentListenAddress,
		AgentAdvertiseURL:       defaultAgentAdvertiseURL,
		AgentArtifactsDirectory: defaultAgentArtifactsDirectory,
		DatabasePath:            defaultDatabasePath,
		OfflineAfter:            45 * time.Second, ShutdownTimeout: 10 * time.Second, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: defaultMaxHeaderBytes,
	}
}

func LoadConfig(path string) (Config, error) { return loadConfig(path, os.LookupEnv) }

func loadConfig(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	config := DefaultConfig()
	if path != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read server config: %w", err)
		}
		var fromFile fileConfig
		decoder := json.NewDecoder(strings.NewReader(string(contents)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fromFile); err != nil {
			return Config{}, fmt.Errorf("decode server config: %w", err)
		}
		if err := applyFileConfig(&config, fromFile); err != nil {
			return Config{}, err
		}
	}
	applyStringEnv(&config.ManagementListenAddress, lookupEnv, "SETPOINT_SERVER_MANAGEMENT_LISTEN")
	applyStringEnv(&config.AgentListenAddress, lookupEnv, "SETPOINT_SERVER_AGENT_LISTEN")
	applyStringEnv(&config.AgentAdvertiseURL, lookupEnv, "SETPOINT_SERVER_AGENT_ADVERTISE_URL")
	applyStringEnv(&config.AgentArtifactsDirectory, lookupEnv, "SETPOINT_SERVER_AGENT_ARTIFACTS_DIR")
	applyStringEnv(&config.DatabasePath, lookupEnv, "SETPOINT_SERVER_DATABASE_PATH")
	for name, target := range map[string]*time.Duration{"SETPOINT_SERVER_OFFLINE_AFTER": &config.OfflineAfter, "SETPOINT_SERVER_SHUTDOWN_TIMEOUT": &config.ShutdownTimeout, "SETPOINT_SERVER_READ_HEADER_TIMEOUT": &config.ReadHeaderTimeout, "SETPOINT_SERVER_IDLE_TIMEOUT": &config.IdleTimeout} {
		if value, exists := lookupEnv(name); exists {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", name, err)
			}
			*target = parsed
		}
	}
	if value, exists := lookupEnv("SETPOINT_SERVER_MAX_HEADER_BYTES"); exists {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse SETPOINT_SERVER_MAX_HEADER_BYTES: %w", err)
		}
		config.MaxHeaderBytes = parsed
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	if err := validateManagementListenAddress(config.ManagementListenAddress); err != nil {
		return err
	}
	if strings.TrimSpace(config.AgentListenAddress) == "" {
		return errors.New("Agent listen address is required")
	}
	if _, _, err := net.SplitHostPort(config.AgentListenAddress); err != nil {
		return fmt.Errorf("validate Agent listen address: %w", err)
	}
	if config.ManagementListenAddress == config.AgentListenAddress {
		return errors.New("management and Agent listen addresses must differ")
	}
	if err := bootstrap.ValidateAgentAdvertiseURL(config.AgentAdvertiseURL); err != nil {
		return fmt.Errorf("validate Agent advertise URL: %w", err)
	}
	if strings.TrimSpace(config.AgentArtifactsDirectory) == "" {
		return errors.New("Agent artifacts directory is required")
	}
	if strings.TrimSpace(config.DatabasePath) == "" {
		return errors.New("server database path is required")
	}
	if config.OfflineAfter <= 0 || config.ShutdownTimeout <= 0 || config.ReadHeaderTimeout <= 0 || config.IdleTimeout <= 0 {
		return errors.New("server durations must be positive")
	}
	if config.MaxHeaderBytes <= 0 {
		return errors.New("server max header bytes must be positive")
	}
	return nil
}

func validateManagementListenAddress(address string) error {
	if strings.TrimSpace(address) == "" {
		return errors.New("management listen address is required")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("validate management listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("management listener must bind a literal loopback address")
	}
	return nil
}

func applyFileConfig(config *Config, fromFile fileConfig) error {
	if fromFile.ManagementListenAddress != "" {
		config.ManagementListenAddress = fromFile.ManagementListenAddress
	}
	if fromFile.AgentListenAddress != "" {
		config.AgentListenAddress = fromFile.AgentListenAddress
	}
	if fromFile.AgentAdvertiseURL != "" {
		config.AgentAdvertiseURL = fromFile.AgentAdvertiseURL
	}
	if fromFile.AgentArtifactsDirectory != "" {
		config.AgentArtifactsDirectory = fromFile.AgentArtifactsDirectory
	}
	if fromFile.DatabasePath != "" {
		config.DatabasePath = fromFile.DatabasePath
	}
	if fromFile.MaxHeaderBytes != 0 {
		config.MaxHeaderBytes = fromFile.MaxHeaderBytes
	}
	for name, value := range map[string]struct {
		input  string
		target *time.Duration
	}{"offline_after": {fromFile.OfflineAfter, &config.OfflineAfter}, "shutdown_timeout": {fromFile.ShutdownTimeout, &config.ShutdownTimeout}, "read_header_timeout": {fromFile.ReadHeaderTimeout, &config.ReadHeaderTimeout}, "idle_timeout": {fromFile.IdleTimeout, &config.IdleTimeout}} {
		if value.input == "" {
			continue
		}
		parsed, err := time.ParseDuration(value.input)
		if err != nil {
			return fmt.Errorf("parse server config %s: %w", name, err)
		}
		*value.target = parsed
	}
	return nil
}
func applyStringEnv(target *string, lookupEnv func(string) (string, bool), name string) {
	if value, exists := lookupEnv(name); exists {
		*target = value
	}
}
