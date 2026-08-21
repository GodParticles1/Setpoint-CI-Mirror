package executor

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type PipelineResult struct {
	Source           Result `json:"source"`
	Target           Result `json:"target"`
	BytesTransferred int64  `json:"bytes_transferred"`
}

type PipelineExecutor interface {
	ExecutePipeline(context.Context, Command, Command) (PipelineResult, error)
}

type PipelineError struct {
	Stage  string
	Result PipelineResult
	Err    error
}

func (err *PipelineError) Error() string {
	return fmt.Sprintf("pipeline %s failed: %v", err.Stage, err.Err)
}

func (err *PipelineError) Unwrap() error { return err.Err }

func (executor *OSExecutor) ExecutePipeline(ctx context.Context, sourceCommand, targetCommand Command) (PipelineResult, error) {
	if err := validateCommand(sourceCommand); err != nil {
		return PipelineResult{}, fmt.Errorf("validate source command: %w", err)
	}
	if err := validateCommand(targetCommand); err != nil {
		return PipelineResult{}, fmt.Errorf("validate target command: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PipelineResult{}, err
	}

	sourceExecutable, err := executor.resolveExecutable(sourceCommand.Name)
	if err != nil {
		return PipelineResult{}, &PipelineError{Stage: "source_start", Err: err}
	}
	targetExecutable, err := executor.resolveExecutable(targetCommand.Name)
	if err != nil {
		return PipelineResult{}, &PipelineError{Stage: "target_start", Err: err}
	}

	startedAt := time.Now()
	source := exec.CommandContext(ctx, sourceExecutable, sourceCommand.Args...)
	target := exec.CommandContext(ctx, targetExecutable, targetCommand.Args...)

	sourceStderr := &limitedBuffer{maximum: executor.OutputLimit}
	targetStdout := &limitedBuffer{maximum: executor.OutputLimit}
	targetStderr := &limitedBuffer{maximum: executor.OutputLimit}
	source.Stderr = sourceStderr
	target.Stdout = targetStdout
	target.Stderr = targetStderr

	pipeReader, pipeWriter := io.Pipe()
	counter := &countingWriter{writer: pipeWriter}
	source.Stdout = counter
	target.Stdin = pipeReader

	if err := target.Start(); err != nil {
		_ = pipeReader.Close()
		_ = pipeWriter.Close()
		return PipelineResult{}, &PipelineError{Stage: "target_start", Err: err}
	}
	if err := source.Start(); err != nil {
		_ = pipeWriter.CloseWithError(err)
		_ = target.Process.Kill()
		_ = target.Wait()
		_ = pipeReader.Close()
		return PipelineResult{}, &PipelineError{Stage: "source_start", Err: err}
	}

	sourceErr := source.Wait()
	if sourceErr != nil {
		_ = pipeWriter.CloseWithError(sourceErr)
	} else {
		_ = pipeWriter.Close()
	}
	targetErr := target.Wait()
	_ = pipeReader.Close()

	elapsed := time.Since(startedAt)
	result := PipelineResult{
		Source: Result{Stderr: sourceStderr.String(), ExitCode: exitCode(source), Duration: elapsed, StderrTruncated: sourceStderr.truncated},
		Target: Result{Stdout: targetStdout.String(), Stderr: targetStderr.String(), ExitCode: exitCode(target), Duration: elapsed, StdoutTruncated: targetStdout.truncated, StderrTruncated: targetStderr.truncated},
		BytesTransferred: counter.count,
	}

	if ctx.Err() != nil {
		return result, &PipelineError{Stage: "context", Result: result, Err: ctx.Err()}
	}
	if sourceErr != nil {
		return result, &PipelineError{Stage: "source", Result: result, Err: sourceErr}
	}
	if targetErr != nil {
		return result, &PipelineError{Stage: "target", Result: result, Err: targetErr}
	}
	return result, nil
}

func exitCode(command *exec.Cmd) int {
	if command.ProcessState == nil {
		return -1
	}
	return command.ProcessState.ExitCode()
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(buffer []byte) (int, error) {
	written, err := writer.writer.Write(buffer)
	writer.count += int64(written)
	return written, err
}

var _ PipelineExecutor = (*OSExecutor)(nil)
