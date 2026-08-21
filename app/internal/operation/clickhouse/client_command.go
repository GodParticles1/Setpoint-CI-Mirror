package clickhouse

import (
	"errors"
	"strings"

	"setpoint/internal/executor"
)

// ClientCommand describes how the local ClickHouse CLI is launched. Some KE
// packages ship clickhouse-client, while newer/non-root packages may only ship
// the unified clickhouse binary and require the "client" subcommand.
type ClientCommand struct {
	Name       string   `json:"name"`
	PrefixArgs []string `json:"prefix_args,omitempty"`
}

func DefaultClientCommand() ClientCommand {
	return ClientCommand{Name: "clickhouse-client"}
}

func ClassicClientCommand(path string) ClientCommand {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "clickhouse-client"
	}
	return ClientCommand{Name: path}
}

func UnifiedClientCommand(path string) ClientCommand {
	return ClientCommand{Name: strings.TrimSpace(path), PrefixArgs: []string{"client"}}
}

func (command ClientCommand) Validate() error {
	if strings.TrimSpace(command.Name) == "" {
		return errors.New("ClickHouse client command name is required")
	}
	if strings.ContainsRune(command.Name, 0) {
		return errors.New("ClickHouse client command name contains NUL")
	}
	for _, arg := range command.PrefixArgs {
		if strings.ContainsRune(arg, 0) {
			return errors.New("ClickHouse client prefix argument contains NUL")
		}
	}
	return nil
}

func (command ClientCommand) Build(args ...string) (executor.Command, error) {
	if err := command.Validate(); err != nil {
		return executor.Command{}, err
	}
	allArgs := make([]string, 0, len(command.PrefixArgs)+len(args))
	allArgs = append(allArgs, command.PrefixArgs...)
	allArgs = append(allArgs, args...)
	return executor.Command{Name: command.Name, Args: allArgs}, nil
}
