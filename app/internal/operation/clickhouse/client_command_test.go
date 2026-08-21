package clickhouse

import (
	"context"
	"reflect"
	"testing"

	"setpoint/internal/executor"
)

type captureCommandExecutor struct {
	command executor.Command
	result  executor.Result
}

func (capture *captureCommandExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	capture.command = command
	return capture.result, nil
}

func TestUnifiedClientCommandPrefixesClientSubcommand(t *testing.T) {
	command, err := UnifiedClientCommand("/opt/clickhouse").Build("--query", "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if command.Name != "/opt/clickhouse" {
		t.Fatalf("name=%q", command.Name)
	}
	want := []string{"client", "--query", "SELECT 1"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("args=%v want=%v", command.Args, want)
	}
}

func TestClassicClientCommandKeepsTraditionalInvocation(t *testing.T) {
	command, err := ClassicClientCommand("/root/bin/clickhouse-client").Build("--query", "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if command.Name != "/root/bin/clickhouse-client" {
		t.Fatalf("name=%q", command.Name)
	}
	want := []string{"--query", "SELECT 1"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("args=%v want=%v", command.Args, want)
	}
}

func TestExecutorClientUsesConfiguredUnifiedCommand(t *testing.T) {
	capture := &captureCommandExecutor{result: executor.Result{Stdout: "23.12.1.9\n", ExitCode: 0}}
	client, err := NewExecutorClientWithCommand(capture, UnifiedClientCommand("/home/app/opt/xbrother/clickhouse/bin/clickhouse"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Query(context.Background(), QueryRequest{Host: "127.0.0.1", Port: 9000, Query: "SELECT version()", Format: FormatTSVRaw})
	if err != nil {
		t.Fatal(err)
	}
	if got != "23.12.1.9" {
		t.Fatalf("result=%q", got)
	}
	if capture.command.Name != "/home/app/opt/xbrother/clickhouse/bin/clickhouse" || len(capture.command.Args) == 0 || capture.command.Args[0] != "client" {
		t.Fatalf("command=%#v", capture.command)
	}
}
