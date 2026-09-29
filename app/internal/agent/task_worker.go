package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

type TaskRemote interface {
	ClaimTask(context.Context, string) (*task.Resource, error)
	AcknowledgeTask(context.Context, string, string, string) (task.Resource, error)
	SubmitTaskResult(context.Context, string, string, task.ResultSubmission) (task.Resource, error)
}

type TaskProcessor interface{ ProcessOne(context.Context) error }

type taskRemoteError struct {
	operation string
	err       error
}

func (err *taskRemoteError) Error() string { return fmt.Sprintf("%s: %v", err.operation, err.err) }
func (err *taskRemoteError) Unwrap() error { return err.err }

type fatalTaskError struct{ err error }

func (err *fatalTaskError) Error() string { return err.err.Error() }
func (err *fatalTaskError) Unwrap() error { return err.err }
func isFatalTaskError(err error) bool {
	var fatal *fatalTaskError
	return errors.As(err, &fatal)
}

type TaskWorker struct {
	remote         TaskRemote
	agentID        string
	system         string
	registry       *plugin.CheckRegistry
	operations     *operation.Registry
	execution      OperationExecutionRunner
	executor       executor.CommandExecutor
	journal        *TaskJournal
	commandTimeout time.Duration
	now            func() time.Time
}

func NewTaskWorker(remote TaskRemote, agentID, system string, registry *plugin.CheckRegistry, commandExecutor executor.CommandExecutor, journal *TaskJournal, commandTimeout time.Duration) (*TaskWorker, error) {
	return newTaskWorker(remote, agentID, system, registry, nil, nil, commandExecutor, journal, commandTimeout)
}

func NewTaskWorkerWithOperations(remote TaskRemote, agentID, system string, registry *plugin.CheckRegistry, operations *operation.Registry, commandExecutor executor.CommandExecutor, journal *TaskJournal, commandTimeout time.Duration) (*TaskWorker, error) {
	if operations == nil {
		return nil, errors.New("operation registry is required")
	}
	return newTaskWorker(remote, agentID, system, registry, operations, nil, commandExecutor, journal, commandTimeout)
}

func NewTaskWorkerWithControlledOperations(remote TaskRemote, agentID, system string, registry *plugin.CheckRegistry, operations *operation.Registry, execution OperationExecutionRunner, commandExecutor executor.CommandExecutor, journal *TaskJournal, commandTimeout time.Duration) (*TaskWorker, error) {
	if operations == nil || execution == nil {
		return nil, errors.New("operation registry and bounded action runner are required")
	}
	return newTaskWorker(remote, agentID, system, registry, operations, execution, commandExecutor, journal, commandTimeout)
}

func newTaskWorker(remote TaskRemote, agentID, system string, registry *plugin.CheckRegistry, operations *operation.Registry, execution OperationExecutionRunner, commandExecutor executor.CommandExecutor, journal *TaskJournal, commandTimeout time.Duration) (*TaskWorker, error) {
	if remote == nil || registry == nil || commandExecutor == nil || journal == nil {
		return nil, errors.New("task remote, registry, executor and journal are required")
	}
	if agentID == "" || system == "" {
		return nil, errors.New("task worker Agent ID and system are required")
	}
	if commandTimeout <= 0 {
		return nil, errors.New("task command timeout must be positive")
	}
	return &TaskWorker{remote: remote, agentID: agentID, system: system, registry: registry, operations: operations, execution: execution, executor: commandExecutor, journal: journal, commandTimeout: commandTimeout, now: time.Now}, nil
}

func (worker *TaskWorker) ProcessOne(ctx context.Context) error {
	entry, found, err := worker.journal.Load()
	if err != nil {
		return &fatalTaskError{err: err}
	}
	if found {
		return worker.resume(ctx, entry)
	}
	resource, err := worker.remote.ClaimTask(ctx, worker.agentID)
	if err != nil {
		return &taskRemoteError{operation: "claim task", err: err}
	}
	if resource == nil {
		return nil
	}
	entry = taskJournalEntry{Version: 1, State: journalClaimed, Task: task.Clone(*resource)}
	switch resource.Status.Phase {
	case task.PhaseClaimed:
		if err := worker.journal.Save(entry); err != nil {
			return &fatalTaskError{err: err}
		}
		return worker.executeClaimed(ctx, entry)
	case task.PhaseRunning:
		return worker.cacheAndSubmit(ctx, *resource, worker.interruptedSubmission(*resource))
	case task.PhaseCancelRequested:
		return worker.cacheAndSubmit(ctx, *resource, worker.canceledSubmission(*resource))
	default:
		return &fatalTaskError{err: fmt.Errorf("claimed task %s has unsupported phase %s", resource.Metadata.ID, resource.Status.Phase)}
	}
}

func (worker *TaskWorker) resume(ctx context.Context, entry taskJournalEntry) error {
	switch entry.State {
	case journalClaimed:
		return worker.executeClaimed(ctx, entry)
	case journalExecuting:
		return worker.cacheAndSubmit(ctx, entry.Task, worker.interruptedSubmission(entry.Task))
	case journalCompleted:
		return worker.submitCached(ctx, entry)
	default:
		return &fatalTaskError{err: fmt.Errorf("unsupported task journal state %q", entry.State)}
	}
}

func (worker *TaskWorker) executeClaimed(ctx context.Context, entry taskJournalEntry) error {
	resource, err := worker.remote.AcknowledgeTask(ctx, worker.agentID, entry.Task.Metadata.ID, entry.Task.Status.ClaimID)
	if err != nil {
		return &taskRemoteError{operation: "acknowledge task", err: err}
	}
	entry.Task = task.Clone(resource)
	if resource.Status.Phase == task.PhaseCancelRequested {
		return worker.cacheAndSubmit(ctx, resource, worker.canceledSubmission(resource))
	}
	if resource.Status.Phase != task.PhaseRunning {
		return &fatalTaskError{err: fmt.Errorf("acknowledged task %s has phase %s", resource.Metadata.ID, resource.Status.Phase)}
	}
	entry.State = journalExecuting
	if err := worker.journal.Save(entry); err != nil {
		return &fatalTaskError{err: err}
	}

	executionContext, cancel := context.WithTimeout(ctx, worker.commandTimeout)
	defer cancel()
	switch resource.Kind {
	case task.KindReadOnlyCheckTask:
		result, lifecycleErr := worker.executeTaskCheck(executionContext, resource)
		if lifecycleErr != nil {
			pluginID, pluginVersion := worker.taskPluginIdentity(resource)
			result = worker.failureResult(pluginID, pluginVersion, "plugin_execution_unavailable", lifecycleErr.Error())
		}
		phase := task.PhaseSucceeded
		if result.State == task.CheckError {
			phase = task.PhaseFailed
		}
		return worker.cacheAndSubmit(ctx, resource, task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: phase, Result: &result})
	case task.KindOperationPlanningTask:
		return worker.executeOperationPlanning(ctx, executionContext, resource)
	case task.KindOperationExecutionTask:
		return worker.executeOperationAction(ctx, executionContext, resource)
	default:
		return &fatalTaskError{err: fmt.Errorf("task %s has unsupported kind %q", resource.Metadata.ID, resource.Kind)}
	}
}

func (worker *TaskWorker) executeOperationPlanning(ctx, executionContext context.Context, resource task.Resource) error {
	if worker.operations == nil {
		return worker.cacheAndSubmit(ctx, resource, worker.operationFailureSubmission(resource, "operation_registry_unavailable", errors.New("Agent has no operation planning registry")))
	}
	definition, ok := worker.operations.PlanningDefinition(resource.Spec.OperationID)
	if !ok {
		return worker.cacheAndSubmit(ctx, resource, worker.operationFailureSubmission(resource, "operation_definition_unavailable", fmt.Errorf("operation %q is not registered", resource.Spec.OperationID)))
	}
	metadata := definition.Metadata()
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil || metadata.Version != resource.Spec.OperationVersion || digest != resource.Spec.CapabilityDigest {
		if err == nil {
			err = errors.New("registered operation does not match the frozen task contract")
		}
		return worker.cacheAndSubmit(ctx, resource, worker.operationFailureSubmission(resource, "operation_contract_mismatch", err))
	}
	if len(resource.Spec.SecretRefs) > 0 {
		result := worker.operationBlockedResult(resource, "secret_delivery_unavailable", "runtime SecretRef delivery is not implemented")
		return worker.cacheAndSubmit(ctx, resource, task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: task.PhaseSucceeded, OperationResult: &result})
	}
	result := operation.ExecutePlanning(executionContext, definition, operation.RuntimeInput{Executor: worker.executor, Parameters: resource.Spec.Parameters, System: worker.system, Targets: resource.Spec.Targets, SecretRefs: resource.Spec.SecretRefs}, worker.now)
	phase := task.PhaseSucceeded
	if result.State == operation.StateInterrupted {
		phase = task.PhaseFailed
	}
	return worker.cacheAndSubmit(ctx, resource, task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: phase, OperationResult: &result})
}

func (worker *TaskWorker) executeOperationAction(ctx, executionContext context.Context, resource task.Resource) error {
	if worker.execution == nil {
		return worker.cacheAndSubmit(ctx, resource, worker.executionFailureSubmission(resource, "operation_execution_unavailable", errors.New("Agent has no bounded action runner")))
	}
	if len(resource.Spec.SecretRefs) > 0 {
		return worker.cacheAndSubmit(ctx, resource, worker.executionFailureSubmission(resource, "secret_delivery_unavailable", errors.New("runtime SecretRef delivery is unavailable")))
	}
	result, err := worker.execution.Execute(executionContext, resource)
	phase := task.PhaseSucceeded
	if err != nil || result.Error != nil {
		phase = task.PhaseFailed
	}
	return worker.cacheAndSubmit(ctx, resource, task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: phase, OperationExecutionResult: &result})
}

func (worker *TaskWorker) cacheAndSubmit(ctx context.Context, resource task.Resource, submission task.ResultSubmission) error {
	entry := taskJournalEntry{Version: 1, State: journalCompleted, Task: task.Clone(resource), Submission: &submission}
	if err := worker.journal.Save(entry); err != nil {
		return &fatalTaskError{err: err}
	}
	return worker.submitCached(ctx, entry)
}

func (worker *TaskWorker) submitCached(ctx context.Context, entry taskJournalEntry) error {
	if entry.Submission == nil {
		return &fatalTaskError{err: errors.New("cached task result is missing")}
	}
	current, err := worker.remote.ClaimTask(ctx, worker.agentID)
	if err != nil {
		return &taskRemoteError{operation: "refresh task before result submission", err: err}
	}
	if current != nil && current.Metadata.ID == entry.Task.Metadata.ID && current.Status.Phase == task.PhaseCancelRequested && entry.Submission.Phase != task.PhaseCanceled {
		entry.Task = task.Clone(*current)
		if entry.Task.Kind != task.KindOperationExecutionTask || entry.Submission.OperationExecutionResult == nil {
			submission := worker.canceledSubmission(*current)
			entry.Submission = &submission
		}
		if err := worker.journal.Save(entry); err != nil {
			return &fatalTaskError{err: err}
		}
	}
	if _, err := worker.remote.SubmitTaskResult(ctx, worker.agentID, entry.Task.Metadata.ID, *entry.Submission); err != nil {
		return &taskRemoteError{operation: "submit task result", err: err}
	}
	if err := worker.journal.Clear(); err != nil {
		return &fatalTaskError{err: err}
	}
	return nil
}

func (worker *TaskWorker) pluginVersion(pluginID string) string {
	if metadata, exists := worker.registry.Get(pluginID); exists {
		return metadata.Version
	}
	return "unavailable"
}

func (worker *TaskWorker) failureResult(pluginID, pluginVersion, code, message string) task.CheckResult {
	now := worker.now().UTC()
	return task.CheckResult{PluginID: pluginID, PluginVersion: pluginVersion, State: task.CheckError, StartedAt: now, CompletedAt: now, Items: []task.CheckItem{}, Error: &task.Failure{Code: code, Message: message}}
}

func (worker *TaskWorker) interruptedSubmission(resource task.Resource) task.ResultSubmission {
	if resource.Kind == task.KindOperationPlanningTask {
		return worker.operationTerminalSubmission(resource, task.PhaseFailed, operation.StateInterrupted, "agent_execution_interrupted", "Agent stopped after planning began; task was not run again")
	}
	if resource.Kind == task.KindOperationExecutionTask {
		return worker.executionTerminalSubmission(resource, task.PhaseFailed, "agent_execution_interrupted", "Agent stopped after bounded action began; action was not run again")
	}
	now := worker.now().UTC()
	pluginID, pluginVersion := worker.taskPluginIdentity(resource)
	return task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: task.PhaseFailed, Result: &task.CheckResult{PluginID: pluginID, PluginVersion: pluginVersion, State: task.CheckError, StartedAt: now, CompletedAt: now, Items: []task.CheckItem{}, Error: &task.Failure{Code: "agent_execution_interrupted", Message: "Agent stopped after execution began; task was not run again"}}}
}

func (worker *TaskWorker) canceledSubmission(resource task.Resource) task.ResultSubmission {
	if resource.Kind == task.KindOperationPlanningTask {
		return worker.operationTerminalSubmission(resource, task.PhaseCanceled, operation.StateCanceledBeforeApply, "task_canceled", "operation planning was canceled before Apply")
	}
	if resource.Kind == task.KindOperationExecutionTask {
		return worker.executionTerminalSubmission(resource, task.PhaseCanceled, "task_canceled", "bounded operation action was canceled before execution")
	}
	now := worker.now().UTC()
	pluginID, pluginVersion := worker.taskPluginIdentity(resource)
	return task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: task.PhaseCanceled, Result: &task.CheckResult{PluginID: pluginID, PluginVersion: pluginVersion, State: task.CheckError, StartedAt: now, CompletedAt: now, Items: []task.CheckItem{}, Error: &task.Failure{Code: "task_canceled", Message: "task was canceled before plugin execution"}}}
}

func (worker *TaskWorker) executionFailureSubmission(resource task.Resource, code string, err error) task.ResultSubmission {
	return worker.executionTerminalSubmission(resource, task.PhaseFailed, code, err.Error())
}

func (worker *TaskWorker) executionTerminalSubmission(resource task.Resource, phase task.Phase, code, message string) task.ResultSubmission {
	result := task.OperationExecutionResult{Error: &task.Failure{Code: code, Message: message}}
	if resource.Spec.OperationExecution != nil {
		contract := resource.Spec.OperationExecution
		result.OperationID = contract.OperationID
		result.RunID = contract.RunID
		result.Action = contract.Action
		result.ParticipantNodeIDs = append([]string(nil), contract.ParticipantNodeIDs...)
		result.StageID = contract.Stage.ID
		result.StageIndex = contract.StageIndex
		result.ExecutorNodeID = contract.Stage.ExecutorNodeID
	}
	return task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: phase, OperationExecutionResult: &result}
}

func (worker *TaskWorker) operationFailureSubmission(resource task.Resource, code string, err error) task.ResultSubmission {
	now := worker.now().UTC()
	result := operation.PlanningResult{OperationID: resource.Spec.OperationID, OperationVersion: resource.Spec.OperationVersion, CapabilityDigest: resource.Spec.CapabilityDigest, State: operation.StateBlocked, Checkpoint: code, StartedAt: now, CompletedAt: now, Block: &operation.Block{Code: code, Message: err.Error(), SafeNext: "inspect_the_failure_and_create_a_new_run", ManualReview: true}, Error: &operation.PlanningFailure{Code: code, Message: err.Error()}}
	return task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: task.PhaseSucceeded, OperationResult: &result}
}

func (worker *TaskWorker) operationBlockedResult(resource task.Resource, code, message string) operation.PlanningResult {
	now := worker.now().UTC()
	return operation.PlanningResult{OperationID: resource.Spec.OperationID, OperationVersion: resource.Spec.OperationVersion, CapabilityDigest: resource.Spec.CapabilityDigest, State: operation.StateBlocked, Checkpoint: code, StartedAt: now, CompletedAt: now, Block: &operation.Block{Code: code, Message: message, SafeNext: "use_a_verified_runtime_secret_delivery_channel", ManualReview: true}}
}

func (worker *TaskWorker) operationTerminalSubmission(resource task.Resource, phase task.Phase, state operation.State, code, message string) task.ResultSubmission {
	now := worker.now().UTC()
	result := operation.PlanningResult{OperationID: resource.Spec.OperationID, OperationVersion: resource.Spec.OperationVersion, CapabilityDigest: resource.Spec.CapabilityDigest, State: state, Checkpoint: code, StartedAt: now, CompletedAt: now}
	if phase != task.PhaseCanceled {
		result.Error = &operation.PlanningFailure{Code: code, Message: message}
	}
	return task.ResultSubmission{ClaimID: resource.Status.ClaimID, Phase: phase, OperationResult: &result}
}
