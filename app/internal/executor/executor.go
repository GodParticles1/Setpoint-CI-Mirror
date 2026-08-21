package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"setpoint/internal/trustedexec"
)

const (
	defaultOutputLimit = 64 << 10
	MaxOutputLimit     = 1 << 20
)

type Command struct {
	Name        string   `json:"name"`
	Args        []string `json:"args"`
	OutputLimit int      `json:"-"`
}

type Result struct {
	Stdout          string        `json:"stdout"`
	Stderr          string        `json:"stderr"`
	ExitCode        int           `json:"exit_code"`
	Duration        time.Duration `json:"duration"`
	StdoutTruncated bool          `json:"stdout_truncated"`
	StderrTruncated bool          `json:"stderr_truncated"`
}

type ErrorKind string

const (
	ErrorStart    ErrorKind = "command_start_failed"
	ErrorExit     ErrorKind = "command_exit_nonzero"
	ErrorCanceled ErrorKind = "command_canceled"
	ErrorTimeout  ErrorKind = "command_timed_out"
)

type Error struct {
	Kind   ErrorKind
	Result Result
	Err    error
}

func (err *Error) Error() string {
	return fmt.Sprintf("%s: %v", err.Kind, err.Err)
}

func (err *Error) Unwrap() error {
	return err.Err
}

type CommandExecutor interface {
	Execute(context.Context, Command) (Result, error)
}

type OSExecutor struct {
	OutputLimit        int
	trustedDirectories []string
	approvedRoots      []trustedexec.Root
	executableResolver func(string) (string, error)
}

func NewOSExecutor(outputLimit int) (*OSExecutor, error) {
	if outputLimit == 0 {
		outputLimit = defaultOutputLimit
	}
	if outputLimit < 1 || outputLimit > MaxOutputLimit {
		return nil, errors.New("command output limit must be between 1 byte and 1 MiB")
	}
	return &OSExecutor{
		OutputLimit: outputLimit, trustedDirectories: defaultTrustedCommandDirectories(),
	}, nil
}

func (executor *OSExecutor) Execute(ctx context.Context, command Command) (Result, error) {
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, classifyContextError(ctx, Result{}, err)
	}
	startedAt := time.Now()
	var executable string
	var err error
	if executor.executableResolver != nil {
		executable, err = executor.executableResolver(command.Name)
	} else {
		executable, err = executor.resolveExecutable(command.Name)
	}
	if err != nil {
		result := Result{ExitCode: -1, Duration: time.Since(startedAt)}
		return result, &Error{Kind: ErrorStart, Result: result, Err: err}
	}
	process := exec.CommandContext(ctx, executable, command.Args...)
	process.Stdin = nil
	outputLimit := executor.OutputLimit
	if command.OutputLimit > 0 {
		outputLimit = command.OutputLimit
	}
	stdout := &limitedBuffer{maximum: outputLimit}
	stderr := &limitedBuffer{maximum: outputLimit}
	process.Stdout = stdout
	process.Stderr = stderr
	err = process.Run()
	result := Result{
		Stdout: stdout.String(), Stderr: stderr.String(), Duration: time.Since(startedAt),
		StdoutTruncated: stdout.truncated, StderrTruncated: stderr.truncated,
	}
	if process.ProcessState != nil {
		result.ExitCode = process.ProcessState.ExitCode()
	} else {
		result.ExitCode = -1
	}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, classifyContextError(ctx, result, err)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return result, &Error{Kind: ErrorExit, Result: result, Err: err}
	}
	return result, &Error{Kind: ErrorStart, Result: result, Err: err}
}

func validateCommand(command Command) error {
	if strings.TrimSpace(command.Name) == "" {
		return errors.New("command name is required")
	}
	if strings.ContainsRune(command.Name, 0) {
		return errors.New("command name contains NUL")
	}
	for _, argument := range command.Args {
		if strings.ContainsRune(argument, 0) {
			return errors.New("command argument contains NUL")
		}
	}
	if command.OutputLimit < 0 || command.OutputLimit > MaxOutputLimit {
		return errors.New("per-command output limit must be between 1 byte and 1 MiB or zero for the executor default")
	}
	return nil
}

func classifyContextError(ctx context.Context, result Result, err error) error {
	kind := ErrorCanceled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		kind = ErrorTimeout
	}
	return &Error{Kind: kind, Result: result, Err: err}
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	maximum   int
	truncated bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.truncated = buffer.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(value)
	return originalLength, nil
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}
