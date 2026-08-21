package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

type TaskRepository interface {
	CreateTask(context.Context, task.Resource) (task.Resource, bool, error)
	GetTask(context.Context, string) (task.Resource, error)
	ListTasks(context.Context) ([]task.Resource, error)
	ClaimTask(context.Context, string, string, time.Time) (*task.Resource, error)
	AcknowledgeTask(context.Context, string, string, string, time.Time) (task.Resource, error)
	CancelTask(context.Context, string, time.Time) (task.Resource, error)
	CompleteTask(context.Context, string, string, task.ResultSubmission, time.Time) (task.Resource, error)
}

func (service *Service) CreateTask(ctx context.Context, request protocol.CreateTaskRequest) (task.Resource, bool, error) {
	canonical, contract, digest, err := service.validateTaskRequest(ctx, request)
	if err != nil {
		return task.Resource{}, false, &ValidationError{Err: err}
	}
	id, err := task.NewID()
	if err != nil {
		return task.Resource{}, false, err
	}
	createdAt := service.now().UTC()
	resource := task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
		Metadata: task.Metadata{
			ID: id, IdempotencyKey: strings.TrimSpace(request.Metadata.IdempotencyKey), CreatedAt: createdAt,
		},
		Spec: task.Spec{
			NodeID: strings.TrimSpace(request.Spec.NodeID), PluginID: strings.TrimSpace(request.Spec.PluginID), Parameters: canonical,
			Execution: &contract, ContractDigest: digest,
		},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: createdAt},
	}
	created, wasCreated, err := service.nodes.CreateTask(ctx, resource)
	if errors.Is(err, task.ErrIdempotencyConflict) {
		return task.Resource{}, false, &ConflictError{Err: err}
	}
	return created, wasCreated, err
}

func (service *Service) GetTask(ctx context.Context, id string) (task.Resource, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return task.Resource{}, &ValidationError{Err: fmt.Errorf("task id: %w", err)}
	}
	resource, err := service.nodes.GetTask(ctx, id)
	if err == nil && resource.Kind != task.KindReadOnlyCheckTask {
		return task.Resource{}, domain.ErrNotFound
	}
	return resource, err
}

func (service *Service) ListTasks(ctx context.Context) ([]task.Resource, error) {
	resources, err := service.nodes.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	checks := make([]task.Resource, 0, len(resources))
	for _, resource := range resources {
		if resource.Kind == task.KindReadOnlyCheckTask {
			checks = append(checks, resource)
		}
	}
	return checks, nil
}

func (service *Service) ClaimTask(ctx context.Context, agentID string) (*task.Resource, error) {
	agentID = strings.TrimSpace(agentID)
	if err := validateIdentifier(agentID); err != nil {
		return nil, &ValidationError{Err: fmt.Errorf("agent_id: %w", err)}
	}
	claimID, err := task.NewID()
	if err != nil {
		return nil, err
	}
	return service.nodes.ClaimTask(ctx, agentID, claimID, service.now().UTC())
}

func (service *Service) AcknowledgeTask(
	ctx context.Context,
	agentID, taskID string,
	request protocol.AcknowledgeTaskRequest,
) (task.Resource, error) {
	if err := validateTaskAgentRequest(agentID, taskID, request.ClaimID); err != nil {
		return task.Resource{}, &ValidationError{Err: err}
	}
	resource, err := service.nodes.AcknowledgeTask(
		ctx, strings.TrimSpace(agentID), strings.TrimSpace(taskID), strings.TrimSpace(request.ClaimID), service.now().UTC())
	return resource, classifyTaskConflict(err)
}

func (service *Service) CancelTask(ctx context.Context, taskID string) (task.Resource, error) {
	taskID = strings.TrimSpace(taskID)
	if err := validateIdentifier(taskID); err != nil {
		return task.Resource{}, &ValidationError{Err: fmt.Errorf("task id: %w", err)}
	}
	existing, err := service.nodes.GetTask(ctx, taskID)
	if err != nil {
		return task.Resource{}, err
	}
	if existing.Kind != task.KindReadOnlyCheckTask {
		return task.Resource{}, domain.ErrNotFound
	}
	resource, err := service.nodes.CancelTask(ctx, taskID, service.now().UTC())
	return resource, classifyTaskConflict(err)
}

func (service *Service) SubmitTaskResult(
	ctx context.Context,
	agentID, taskID string,
	submission task.ResultSubmission,
) (task.Resource, error) {
	if err := validateTaskAgentRequest(agentID, taskID, submission.ClaimID); err != nil {
		return task.Resource{}, &ValidationError{Err: err}
	}
	existing, err := service.nodes.GetTask(ctx, strings.TrimSpace(taskID))
	if err != nil {
		return task.Resource{}, err
	}
	if err := service.validateSubmittedTaskResult(existing, &submission); err != nil {
		return task.Resource{}, &ValidationError{Err: err}
	}
	resource, err := service.nodes.CompleteTask(
		ctx, strings.TrimSpace(agentID), strings.TrimSpace(taskID), submission, service.now().UTC())
	return resource, classifyTaskConflict(err)
}

func (service *Service) validateSubmittedTaskResult(existing task.Resource, submission *task.ResultSubmission) error {
	switch existing.Kind {
	case task.KindReadOnlyCheckTask:
		contract, err := service.resultContract(existing)
		if err != nil {
			return err
		}
		if task.Terminal(existing.Status.Phase) && existing.Result != nil {
			contract = replayResultContract(*existing.Result)
		}
		return validateTaskResult(submission, contract)
	case task.KindOperationPlanningTask:
		return validateOperationPlanningResult(existing, submission)
	default:
		return fmt.Errorf("unsupported task kind %q", existing.Kind)
	}
}

func validateOperationPlanningResult(resource task.Resource, submission *task.ResultSubmission) error {
	if submission.OperationResult == nil || submission.Result != nil {
		return errors.New("operation planning task requires exactly one operation result")
	}
	if !task.ValidResultPhase(submission.Phase) {
		return errors.New("phase must be succeeded, failed, or canceled")
	}
	if resource.Status.Phase == task.PhaseCancelRequested && submission.Phase != task.PhaseCanceled {
		return errors.New("cancel-requested operation task only accepts a canceled result")
	}
	result := submission.OperationResult
	if result.OperationID != resource.Spec.OperationID || result.OperationVersion != resource.Spec.OperationVersion ||
		result.CapabilityDigest != resource.Spec.CapabilityDigest {
		return errors.New("operation planning result does not match the frozen task contract")
	}
	if result.StartedAt.IsZero() || result.CompletedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) {
		return errors.New("operation planning result has invalid execution timestamps")
	}
	if !operation.ValidState(result.State) {
		return fmt.Errorf("operation planning result has invalid state %q", result.State)
	}
	switch submission.Phase {
	case task.PhaseSucceeded:
		if result.State != operation.StateAwaitingConfirm && result.State != operation.StateBlocked {
			return errors.New("succeeded operation planning task requires an awaiting-confirmation or blocked result")
		}
		if result.State == operation.StateAwaitingConfirm && result.Error != nil {
			return errors.New("awaiting-confirmation result must not contain an error")
		}
		if result.State == operation.StateAwaitingConfirm {
			if err := validatePlanningEvidence(*result); err != nil {
				return err
			}
			digest, err := operation.PlanDigest(resource.Spec.CapabilityDigest, resource.Spec.Targets,
				resource.Spec.Parameters, resource.Spec.SecretRefs, *result.Plan, *result.Impact)
			if err != nil || digest != result.PlanDigest {
				return errors.New("operation planning result has an invalid plan digest")
			}
		}
		if result.State == operation.StateBlocked && result.Block == nil {
			return errors.New("blocked operation planning result requires a block reason")
		}
	case task.PhaseFailed:
		if result.Error == nil || result.State != operation.StateInterrupted {
			return errors.New("failed operation planning task requires an interrupted result")
		}
	case task.PhaseCanceled:
		if result.State != operation.StateCanceledBeforeApply {
			return errors.New("canceled operation planning task requires canceled_before_apply state")
		}
	}
	return nil
}

func validatePlanningEvidence(result operation.PlanningResult) error {
	if result.Checkpoint != "plan_ready" {
		return errors.New("awaiting-confirmation result requires the plan_ready checkpoint")
	}
	if result.Discovery == nil || !result.Discovery.Applicable || len(result.Discovery.Targets) == 0 ||
		!validOperationArtifact(result.Discovery.Snapshot) {
		return errors.New("awaiting-confirmation result requires applicable discovery evidence with physical targets")
	}
	if result.Precheck == nil || !result.Precheck.Passed || !validOperationArtifact(result.Precheck.Snapshot) {
		return errors.New("awaiting-confirmation result requires passed precheck evidence")
	}
	if result.Plan == nil || result.Impact == nil || result.PlanDigest == "" ||
		strings.TrimSpace(result.Plan.SchemaVersion) == "" || !validOperationArtifact(result.Plan.Execution) {
		return errors.New("awaiting-confirmation result requires a versioned plan, execution artifact, impact, and plan digest")
	}
	return nil
}

func validOperationArtifact(artifact operation.Artifact) bool {
	return strings.TrimSpace(artifact.SchemaVersion) != "" && len(artifact.Payload) > 0 && json.Valid(artifact.Payload)
}

func (service *Service) resultContract(resource task.Resource) (task.ResultContract, error) {
	if resource.Spec.Execution != nil {
		if err := task.ValidateCheckExecutionContract(*resource.Spec.Execution, resource.Spec.ContractDigest); err != nil {
			return task.ResultContract{}, fmt.Errorf("task has an invalid frozen execution contract: %w", err)
		}
		return resource.Spec.Execution.ResultContract(), nil
	}
	metadata, exists := service.checks.Get(resource.Spec.PluginID)
	if !exists {
		return task.ResultContract{}, fmt.Errorf("legacy task plugin %q is no longer registered", resource.Spec.PluginID)
	}
	return plugin.ResultContract(metadata), nil
}

func (service *Service) validateTaskRequest(
	ctx context.Context,
	request protocol.CreateTaskRequest,
) (json.RawMessage, task.CheckExecutionContract, string, error) {
	if request.APIVersion != "setpoint.io/v1" {
		return nil, task.CheckExecutionContract{}, "", errors.New("api_version must be setpoint.io/v1")
	}
	if request.Kind != task.KindReadOnlyCheckTask {
		return nil, task.CheckExecutionContract{}, "", errors.New("kind must be ReadOnlyCheckTask")
	}
	key := strings.TrimSpace(request.Metadata.IdempotencyKey)
	if err := validateIdentifier(key); err != nil {
		return nil, task.CheckExecutionContract{}, "", fmt.Errorf("metadata.idempotency_key: %w", err)
	}
	nodeID := strings.TrimSpace(request.Spec.NodeID)
	if err := validateIdentifier(nodeID); err != nil {
		return nil, task.CheckExecutionContract{}, "", fmt.Errorf("spec.node_id: %w", err)
	}
	if _, err := service.nodes.GetNode(ctx, nodeID, service.offlineAfter); err != nil {
		return nil, task.CheckExecutionContract{}, "", fmt.Errorf("spec.node_id: %w", err)
	}
	pluginID := strings.TrimSpace(request.Spec.PluginID)
	if err := validateIdentifier(pluginID); err != nil {
		return nil, task.CheckExecutionContract{}, "", fmt.Errorf("spec.plugin_id: %w", err)
	}
	metadata, exists := service.checks.Get(pluginID)
	if !exists {
		return nil, task.CheckExecutionContract{}, "", errors.New("spec.plugin_id does not identify a registered plugin")
	}
	if metadata.Mode != plugin.ModeReadOnly {
		return nil, task.CheckExecutionContract{}, "", errors.New("only read-only plugins can be scheduled in this phase")
	}
	if !service.checks.SupportsCheckExecution(pluginID) {
		return nil, task.CheckExecutionContract{}, "", errors.New("spec.plugin_id does not provide read-only execution")
	}
	parameters, canonical, err := canonicalParameters(request.Spec.Parameters)
	if err != nil {
		return nil, task.CheckExecutionContract{}, "", err
	}
	if err := validateParameterNames(metadata, parameters); err != nil {
		return nil, task.CheckExecutionContract{}, "", err
	}
	roots, err := service.frozenTrustedRootsForNode(ctx, nodeID)
	if err != nil {
		return nil, task.CheckExecutionContract{}, "", fmt.Errorf("freeze trusted executable roots: %w", err)
	}
	contract, digest, err := plugin.FreezeExecutionContract(metadata, nil, canonical, roots)
	if err != nil {
		return nil, task.CheckExecutionContract{}, "", fmt.Errorf("freeze check execution contract: %w", err)
	}
	return canonical, contract, digest, nil
}

func (service *Service) frozenTrustedRootsForNode(ctx context.Context, nodeID string) ([]trustedexec.Root, error) {
	node, err := service.nodes.GetNode(ctx, nodeID, service.offlineAfter)
	if err != nil {
		return nil, err
	}
	return trustedexec.FreezeConfiguredRoots(node.TrustedExecutableRoots)
}

func canonicalParameters(raw json.RawMessage) (map[string]json.RawMessage, json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parameters); err != nil {
		return nil, nil, errors.New("spec.parameters must be a JSON object")
	}
	if parameters == nil {
		parameters = map[string]json.RawMessage{}
	}
	canonical, err := json.Marshal(parameters)
	if err != nil {
		return nil, nil, fmt.Errorf("canonicalize task parameters: %w", err)
	}
	return parameters, canonical, nil
}

func validateParameterNames(metadata plugin.Metadata, supplied map[string]json.RawMessage) error {
	declared := make(map[string]plugin.Parameter, len(metadata.Parameters))
	for _, parameter := range metadata.Parameters {
		declared[parameter.Name] = parameter
	}
	suppliedNames := make([]string, 0, len(supplied))
	for name := range supplied {
		suppliedNames = append(suppliedNames, name)
	}
	sort.Strings(suppliedNames)
	for _, name := range suppliedNames {
		if _, exists := declared[name]; !exists {
			return fmt.Errorf("spec.parameters contains undeclared parameter %q", name)
		}
	}
	for _, parameter := range metadata.Parameters {
		if parameter.Required {
			if _, exists := supplied[parameter.Name]; !exists {
				return fmt.Errorf("spec.parameters.%s is required", parameter.Name)
			}
		}
	}
	return validateParameterValues(metadata, supplied)
}

func validateParameterValues(metadata plugin.Metadata, supplied map[string]json.RawMessage) error {
	for _, parameter := range metadata.Parameters {
		raw, exists := supplied[parameter.Name]
		switch parameter.Type {
		case "string":
			if exists {
				if err := validateStringParameter(parameter, raw); err != nil {
					return err
				}
			}
		case "integer":
			if exists {
				if err := validateIntegerParameter(parameter.Name, raw); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("spec.parameters.%s declares unsupported type %q", parameter.Name, parameter.Type)
		}
	}
	return nil
}

func validateStringParameter(parameter plugin.Parameter, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("spec.parameters.%s must be a string", parameter.Name)
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("spec.parameters.%s must be a string", parameter.Name)
	}
	if len(parameter.Options) == 0 {
		return nil
	}
	for _, option := range parameter.Options {
		if value == option {
			return nil
		}
	}
	return fmt.Errorf("spec.parameters.%s must be one of the declared options: %s", parameter.Name, strings.Join(parameter.Options, ", "))
}

func validateIntegerParameter(name string, raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("spec.parameters.%s must be an integer", name)
	}
	var value int
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("spec.parameters.%s must be an integer", name)
	}
	return nil
}

func validateTaskAgentRequest(agentID, taskID, claimID string) error {
	for name, value := range map[string]string{
		"agent_id": strings.TrimSpace(agentID), "task_id": strings.TrimSpace(taskID), "claim_id": strings.TrimSpace(claimID),
	} {
		if err := validateIdentifier(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func validateTaskResult(submission *task.ResultSubmission, contract task.ResultContract) error {
	if submission.Result == nil || submission.OperationResult != nil {
		return errors.New("read-only check task requires exactly one check result")
	}
	if !task.ValidResultPhase(submission.Phase) {
		return errors.New("phase must be succeeded, failed, or canceled")
	}
	switch submission.Phase {
	case task.PhaseSucceeded:
		if submission.Result.State != task.CheckCompleted {
			return errors.New("succeeded task requires a completed check")
		}
	case task.PhaseFailed, task.PhaseCanceled:
		if submission.Result.State != task.CheckError {
			return errors.New("failed or canceled task requires an error check")
		}
	}
	return task.ValidateCheckResult(submission.Result, contract)
}

func replayResultContract(result task.CheckResult) task.ResultContract {
	itemIDs := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		itemIDs = append(itemIDs, item.ID)
	}
	return task.ResultContract{
		PluginID: result.PluginID, PluginVersion: result.PluginVersion, ItemIDs: itemIDs,
	}
}

func classifyTaskConflict(err error) error {
	if errors.Is(err, task.ErrInvalidTransition) || errors.Is(err, task.ErrClaimMismatch) ||
		errors.Is(err, task.ErrNodeMismatch) || errors.Is(err, task.ErrResultConflict) {
		return &ConflictError{Err: err}
	}
	return err
}
