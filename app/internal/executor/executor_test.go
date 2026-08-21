package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOSExecutorCapturesOutputExitAndLimits(t *testing.T) {
	executor, err := NewOSExecutor(8)
	if err != nil {
		t.Fatal(err)
	}
	executor.executableResolver = func(string) (string, error) { return os.Args[0], nil }
	result, err := executor.Execute(context.Background(), helperCommand("output"))
	if err != nil {
		t.Fatalf("execute output helper: %v", err)
	}
	if result.Stdout != "stdout-1" || result.Stderr != "stderr-2" || !result.StdoutTruncated || !result.StderrTruncated || result.ExitCode != 0 {
		t.Fatalf("unexpected limited result: %#v", result)
	}

	result, err = executor.Execute(context.Background(), helperCommand("exit"))
	var executionError *Error
	if !errors.As(err, &executionError) || executionError.Kind != ErrorExit || result.ExitCode != 7 {
		t.Fatalf("nonzero result=%#v err=%v", result, err)
	}
}

func TestOSExecutorHonorsTimeoutAndCancellation(t *testing.T) {
	executor, err := NewOSExecutor(1024)
	if err != nil {
		t.Fatal(err)
	}
	executor.executableResolver = func(string) (string, error) { return os.Args[0], nil }
	timeoutContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = executor.Execute(timeoutContext, helperCommand("sleep"))
	var executionError *Error
	if !errors.As(err, &executionError) || executionError.Kind != ErrorTimeout {
		t.Fatalf("timeout error=%v", err)
	}

	canceledContext, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = executor.Execute(canceledContext, helperCommand("output"))
	if !errors.As(err, &executionError) || executionError.Kind != ErrorCanceled {
		t.Fatalf("canceled error=%v", err)
	}
}

func TestOSExecutorUsesDefaultOutputLimitWhenCommandDoesNotOverride(t *testing.T) {
	execution, err := NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}
	if execution.OutputLimit != defaultOutputLimit {
		t.Fatalf("default output limit=%d", execution.OutputLimit)
	}
	execution.executableResolver = func(string) (string, error) { return os.Args[0], nil }
	result, err := execution.Execute(context.Background(), sizedCommand(defaultOutputLimit+1024, defaultOutputLimit+2048))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != defaultOutputLimit || len(result.Stderr) != defaultOutputLimit || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("default command budget was not applied: stdout=%d stderr=%d result=%#v", len(result.Stdout), len(result.Stderr), result)
	}
}

func TestOSExecutorSupportsBoundedPerCommandOutputLimits(t *testing.T) {
	execution, err := NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}
	execution.executableResolver = func(string) (string, error) { return os.Args[0], nil }
	for _, limit := range []int{128 << 10, MaxOutputLimit} {
		result, err := execution.Execute(context.Background(), Command{
			Name: os.Args[0], Args: sizedCommand(96<<10, 96<<10).Args, OutputLimit: limit,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Stdout) != 96<<10 || len(result.Stderr) != 96<<10 || result.StdoutTruncated || result.StderrTruncated {
			t.Fatalf("limit=%d result stdout=%d stderr=%d truncated=%t/%t", limit, len(result.Stdout), len(result.Stderr), result.StdoutTruncated, result.StderrTruncated)
		}
	}
}

func TestPerCommandOutputLimitDoesNotChangeFollowingCommand(t *testing.T) {
	execution, err := NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}
	execution.executableResolver = func(string) (string, error) { return os.Args[0], nil }
	overridden := sizedCommand(96<<10, 0)
	overridden.OutputLimit = 128 << 10
	first, err := execution.Execute(context.Background(), overridden)
	if err != nil || first.StdoutTruncated {
		t.Fatalf("overridden command result=%#v err=%v", first, err)
	}
	second, err := execution.Execute(context.Background(), sizedCommand(96<<10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Stdout) != defaultOutputLimit || !second.StdoutTruncated || execution.OutputLimit != defaultOutputLimit {
		t.Fatalf("executor default was contaminated: stdout=%d result=%#v executor=%d", len(second.Stdout), second, execution.OutputLimit)
	}
}

func TestPerCommandOutputLimitRejectsInvalidValuesBeforeStart(t *testing.T) {
	execution, err := NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}
	execution.executableResolver = func(string) (string, error) {
		t.Fatal("invalid output limit reached executable resolution")
		return "", nil
	}
	for _, limit := range []int{-1, MaxOutputLimit + 1} {
		if _, err := execution.Execute(context.Background(), Command{Name: "test", OutputLimit: limit}); err == nil {
			t.Fatalf("invalid per-command output limit %d was accepted", limit)
		}
	}
}

func TestPerCommandOutputLimitAppliesSeparatelyToStdoutAndStderr(t *testing.T) {
	execution, err := NewOSExecutor(0)
	if err != nil {
		t.Fatal(err)
	}
	execution.executableResolver = func(string) (string, error) { return os.Args[0], nil }
	command := sizedCommand(256, 64)
	command.OutputLimit = 128
	result, err := execution.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Stdout) != 128 || !result.StdoutTruncated || len(result.Stderr) != 64 || result.StderrTruncated {
		t.Fatalf("streams did not use independent limits: %#v", result)
	}
}

func TestPerCommandOutputLimitIsNotJSONControlled(t *testing.T) {
	encoded, err := json.Marshal(Command{Name: "ss", Args: []string{"-lntp"}, OutputLimit: MaxOutputLimit})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "OutputLimit") || strings.Contains(string(encoded), "output_limit") {
		t.Fatalf("internal output limit was serialized: %s", encoded)
	}
	var command Command
	if err := json.Unmarshal([]byte(`{"name":"ss","args":["-lntp"],"output_limit":1048576,"OutputLimit":1048576}`), &command); err != nil {
		t.Fatal(err)
	}
	if command.OutputLimit != 0 {
		t.Fatalf("remote JSON changed internal output limit: %#v", command)
	}
}

func TestOSExecutorRejectsInvalidCommandsWithoutShellInterpretation(t *testing.T) {
	executor, err := NewOSExecutor(1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []Command{{}, {Name: "invalid\x00name"}, {Name: os.Args[0], Args: []string{"invalid\x00argument"}}} {
		if _, err := executor.Execute(context.Background(), command); err == nil {
			t.Fatalf("invalid command %#v was accepted", command)
		}
	}
	_, err = executor.Execute(context.Background(), Command{Name: "definitely-not-a-command;echo", Args: []string{"not interpreted"}})
	var executionError *Error
	if !errors.As(err, &executionError) || executionError.Kind != ErrorStart {
		t.Fatalf("shell-like command error=%v", err)
	}
}

func TestExecutorHelperProcess(t *testing.T) {
	marker := -1
	for index, argument := range os.Args {
		if argument == "--executor-helper" {
			marker = index
			break
		}
	}
	if marker == -1 || marker+1 >= len(os.Args) {
		return
	}
	switch os.Args[marker+1] {
	case "output":
		_, _ = os.Stdout.WriteString("stdout-12345")
		_, _ = os.Stderr.WriteString("stderr-23456")
	case "exit":
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
	case "sized":
		if marker+3 >= len(os.Args) {
			os.Exit(10)
		}
		stdoutBytes, stdoutErr := strconv.Atoi(os.Args[marker+2])
		stderrBytes, stderrErr := strconv.Atoi(os.Args[marker+3])
		if stdoutErr != nil || stderrErr != nil || stdoutBytes < 0 || stderrBytes < 0 {
			os.Exit(11)
		}
		_, _ = os.Stdout.WriteString(strings.Repeat("o", stdoutBytes))
		_, _ = os.Stderr.WriteString(strings.Repeat("e", stderrBytes))
	default:
		if strings.TrimSpace(os.Args[marker+1]) != "" {
			os.Exit(9)
		}
	}
	os.Exit(0)
}

func helperCommand(action string) Command {
	return Command{Name: os.Args[0], Args: []string{"-test.run=TestExecutorHelperProcess", "--", "--executor-helper", action}}
}

func sizedCommand(stdoutBytes, stderrBytes int) Command {
	return Command{Name: os.Args[0], Args: []string{
		"-test.run=TestExecutorHelperProcess", "--", "--executor-helper", "sized",
		strconv.Itoa(stdoutBytes), strconv.Itoa(stderrBytes),
	}}
}
