package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func reconnectHandoff(submission *task.ResultSubmission) (*operation.ReconnectHandoff, error) {
	if submission == nil || submission.OperationExecutionResult == nil {
		return nil, nil
	}
	result := submission.OperationExecutionResult
	switch result.Action {
	case task.OperationActionApply:
		if result.Apply == nil || result.Apply.Reconnect == nil {
			return nil, nil
		}
		if result.Rollback != nil {
			return nil, errors.New("apply reconnect result contains rollback output")
		}
		return result.Apply.Reconnect, nil
	case task.OperationActionRollback:
		if result.Rollback == nil || result.Rollback.Reconnect == nil {
			return nil, nil
		}
		if result.Apply != nil {
			return nil, errors.New("rollback reconnect result contains apply output")
		}
		return result.Rollback.Reconnect, nil
	default:
		if result.Apply != nil && result.Apply.Reconnect != nil || result.Rollback != nil && result.Rollback.Reconnect != nil {
			return nil, errors.New("non-destructive operation result cannot request reconnect")
		}
		return nil, nil
	}
}

func (worker *TaskWorker) cacheReconnectAndReboot(ctx context.Context, resource task.Resource, submission task.ResultSubmission) error {
	handoff, err := reconnectHandoff(&submission)
	if err != nil || handoff == nil || handoff.Validate() != nil || handoff.BootIDAfter != "" {
		if err == nil {
			err = errors.New("operation reconnect handoff is incomplete")
		}
		return worker.cacheAndSubmit(ctx, resource, worker.executionFailureSubmission(resource, "operation_reconnect_contract_invalid", err))
	}
	bootID, err := worker.currentBootID(ctx)
	if err != nil || bootID != handoff.BootIDBefore {
		if err == nil {
			err = errors.New("declared pre-reconnect boot identity does not match the Agent host")
		}
		return worker.failReconnect(ctx, resource, submission, "operation_reconnect_boot_identity_invalid", err)
	}
	entry := taskJournalEntry{Version: 1, State: journalReconnecting, Task: task.Clone(resource), Submission: &submission}
	if err := worker.journal.Save(entry); err != nil {
		return &fatalTaskError{err: err}
	}
	commandContext, cancel := context.WithTimeout(ctx, worker.commandTimeout)
	defer cancel()
	result, rebootErr := worker.executor.Execute(commandContext, executor.Command{Name: "reboot"})
	if rebootErr != nil || result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated {
		if rebootErr == nil {
			rebootErr = fmt.Errorf("reboot command failed or output was truncated (exit %d)", result.ExitCode)
		}
		return worker.failReconnect(ctx, resource, submission, "operation_reconnect_reboot_failed", rebootErr)
	}
	return nil
}

func (worker *TaskWorker) failReconnect(ctx context.Context, resource task.Resource, submission task.ResultSubmission, code string, cause error) error {
	submission.Phase = task.PhaseFailed
	if submission.OperationExecutionResult.Rollback != nil {
		submission.OperationExecutionResult.Rollback.Restored = false
	}
	submission.OperationExecutionResult.Error = &task.Failure{Code: code, Message: cause.Error()}
	return worker.cacheAndSubmit(ctx, resource, submission)
}

func (worker *TaskWorker) resumeReconnect(ctx context.Context, entry taskJournalEntry) error {
	handoff, err := reconnectHandoff(entry.Submission)
	if err != nil || handoff == nil {
		if err == nil {
			err = errors.New("cached reconnect handoff is missing")
		}
		return &fatalTaskError{err: err}
	}
	bootID, err := worker.currentBootID(ctx)
	if err != nil {
		return fmt.Errorf("observe reconnect boot identity: %w", err)
	}
	if bootID == handoff.BootIDBefore {
		return nil
	}
	handoff.BootIDAfter = bootID
	entry.State = journalCompleted
	if err := worker.journal.Save(entry); err != nil {
		return &fatalTaskError{err: err}
	}
	return worker.submitCached(ctx, entry)
}

func (worker *TaskWorker) currentBootID(ctx context.Context) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, worker.commandTimeout)
	defer cancel()
	result, err := worker.executor.Execute(commandContext, executor.Command{Name: "cat", Args: []string{"--", "/proc/sys/kernel/random/boot_id"}})
	if err != nil || result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("boot_id command failed or output was truncated (exit %d)", result.ExitCode)
	}
	value := strings.TrimSpace(result.Stdout)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("boot_id is empty or malformed")
	}
	return value, nil
}

// The reconnect state is reserved for a successful action awaiting boot proof.
// Failed actions are cached as completed failures and never initiate a reboot.
func validateReconnectJournal(entry taskJournalEntry) error {
	if entry.Task.Kind != task.KindOperationExecutionTask || entry.Submission == nil || entry.Submission.Phase != task.PhaseSucceeded || entry.Submission.ClaimID != entry.Task.Status.ClaimID || entry.Submission.OperationExecutionResult == nil {
		return errors.New("reconnecting task journal requires a matching successful operation result")
	}
	result := entry.Submission.OperationExecutionResult
	contract := entry.Task.Spec.OperationExecution
	if contract == nil || result.Error != nil || result.RunID != contract.RunID || result.OperationID != contract.OperationID || result.Action != contract.Action || result.StageID != contract.Stage.ID || result.StageIndex != contract.StageIndex || result.ExecutorNodeID != contract.Stage.ExecutorNodeID || !slices.Equal(result.ParticipantNodeIDs, contract.ParticipantNodeIDs) {
		return errors.New("reconnect journal does not match the bounded action")
	}
	if err := task.ValidateOperationMutationEvidence(*result, entry.Submission.Phase); err != nil {
		return err
	}
	handoff, err := reconnectHandoff(entry.Submission)
	if err != nil {
		return err
	}
	if handoff == nil || handoff.BootIDAfter != "" {
		return errors.New("reconnect journal requires pending boot transition evidence")
	}
	return nil
}
