package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"setpoint/internal/executor"
)

type QueryFormat string

const (
	FormatTSVRaw      QueryFormat = "TSVRaw"
	FormatJSONEachRow QueryFormat = "JSONEachRow"
)

type QueryRequest struct {
	Host     string
	Port     uint16
	User     string
	Secure   bool
	Database string
	Query    string
	Format   QueryFormat
}

type QueryClient interface {
	Query(context.Context, QueryRequest) (string, error)
}

type ExecutorClient struct {
	executor      executor.CommandExecutor
	clientCommand ClientCommand
}

func NewExecutorClient(commandExecutor executor.CommandExecutor) (*ExecutorClient, error) {
	return NewExecutorClientWithCommand(commandExecutor, DefaultClientCommand())
}

func NewExecutorClientWithCommand(commandExecutor executor.CommandExecutor, clientCommand ClientCommand) (*ExecutorClient, error) {
	if commandExecutor == nil {
		return nil, errors.New("command executor is required")
	}
	if err := clientCommand.Validate(); err != nil {
		return nil, err
	}
	return &ExecutorClient{executor: commandExecutor, clientCommand: clientCommand}, nil
}

func (client *ExecutorClient) Query(ctx context.Context, request QueryRequest) (string, error) {
	if strings.TrimSpace(request.Query) == "" {
		return "", errors.New("clickhouse query is required")
	}
	if request.Host == "" {
		request.Host = "127.0.0.1"
	}
	if request.Port == 0 {
		if request.Secure {
			request.Port = 9440
		} else {
			request.Port = 9000
		}
	}
	if request.Format == "" {
		request.Format = FormatJSONEachRow
	}
	if request.Format != FormatTSVRaw && request.Format != FormatJSONEachRow {
		return "", fmt.Errorf("unsupported clickhouse output format %q", request.Format)
	}

	args := []string{
		"--host", request.Host,
		"--port", fmt.Sprintf("%d", request.Port),
		"--query", request.Query,
		"--format", string(request.Format),
	}
	if request.User != "" {
		args = append(args, "--user", request.User)
	}
	if request.Database != "" {
		args = append(args, "--database", request.Database)
	}
	if request.Secure {
		args = append(args, "--secure")
	}

	// Passwords are deliberately not accepted here. Credential-bearing Apply
	// paths remain blocked until Setpoint has a runtime-only SecretRef delivery
	// channel that does not use argv, environment variables, ordinary files or logs.
	command, err := client.clientCommand.Build(args...)
	if err != nil {
		return "", err
	}
	result, err := client.executor.Execute(ctx, command)
	if err != nil {
		return "", fmt.Errorf("ClickHouse client query failed: %w", err)
	}
	return strings.TrimSpace(result.Stdout), nil
}
