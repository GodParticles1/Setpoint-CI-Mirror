package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationrun"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

var (
	ErrOperationPlanDigestConflict     = errors.New("operation plan digest does not match the persisted plan")
	ErrOperationRunIdempotencyConflict = errors.New("operation run idempotency key conflicts with a different frozen specification")
	ErrOperationStateConflict          = errors.New("operation run state does not permit the requested transition")
	ErrProductApplyDisabled            = errors.New("product Apply is disabled")
)

type OperationRunRepository interface {
	CreateOperationRun(context.Context, operationrun.Resource, task.Resource) (operationrun.Resource, bool, error)
	GetOperationRun(context.Context, string) (operationrun.Resource, error)
	ListOperationRuns(context.Context, int, int) ([]operationrun.Resource, error)
	CancelOperationRun(context.Context, string, time.Time) (operationrun.Resource, error)
}

type OperationNodeRepository interface {
	GetNode(context.Context, string, time.Duration) (domain.Node, error)
}

type OperationCatalog interface {
	Get(string) (operation.Metadata, bool)
	List() []operation.Metadata
	NormalizeParameters(string, json.RawMessage) (json.RawMessage, error)
}

type OperationsService struct {
	runs         OperationRunRepository
	nodes        OperationNodeRepository
	catalog      OperationCatalog
	offlineAfter time.Duration
	now          func() time.Time
}

func NewOperationsService(runs OperationRunRepository, nodes OperationNodeRepository, catalog OperationCatalog, offlineAfter time.Duration) (*OperationsService, error) {
	if runs == nil || nodes == nil || catalog == nil {
		return nil, errors.New("operation run store, node store and operation catalog are required")
	}
	if offlineAfter <= 0 {
		return nil, errors.New("offline timeout must be positive")
	}
	return &OperationsService{runs: runs, nodes: nodes, catalog: catalog, offlineAfter: offlineAfter, now: time.Now}, nil
}

func (service *OperationsService) ListOperations() ([]operationrun.DefinitionResource, error) {
	metadata := service.catalog.List()
	result := make([]operationrun.DefinitionResource, 0, len(metadata))
	for _, item := range metadata {
		resource, err := operationDefinitionResource(item)
		if err != nil {
			return nil, err
		}
		result = append(result, resource)
	}
	return result, nil
}

func (service *OperationsService) GetOperation(id string) (operationrun.DefinitionResource, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return operationrun.DefinitionResource{}, &ValidationError{Err: fmt.Errorf("operation id: %w", err)}
	}
	metadata, ok := service.catalog.Get(id)
	if !ok {
		return operationrun.DefinitionResource{}, domain.ErrNotFound
	}
	return operationDefinitionResource(metadata)
}

func operationDefinitionResource(metadata operation.Metadata) (operationrun.DefinitionResource, error) {
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil {
		return operationrun.DefinitionResource{}, err
	}
	return operationrun.DefinitionResource{
		APIVersion: "setpoint.io/v1", Kind: "OperationDefinition", Metadata: metadata, CapabilityDigest: digest,
		Availability: operationrun.Availability{Planning: true, Apply: false, BlockCode: "product_apply_disabled", SecretDelivery: false},
	}, nil
}

func (service *OperationsService) CreateOperationRun(ctx context.Context, request protocol.CreateOperationRunRequest) (operationrun.Resource, bool, error) {
	spec, key, _, err := service.validateCreateOperationRun(ctx, request)
	if err != nil {
		return operationrun.Resource{}, false, &ValidationError{Err: err}
	}
	runID, err := task.NewID()
	if err != nil {
		return operationrun.Resource{}, false, err
	}
	taskID, err := task.NewID()
	if err != nil {
		return operationrun.Resource{}, false, err
	}
	createdAt := service.now().UTC()
	run := operationrun.Resource{
		APIVersion: "setpoint.io/v1", Kind: "OperationRun",
		Metadata: operationrun.Metadata{ID: runID, IdempotencyKey: key, CreatedAt: createdAt}, Spec: spec,
		Status: operationrun.Status{State: operation.StateDraft, Checkpoint: "planning_queued", TaskID: taskID, UpdatedAt: createdAt, ApplyAvailable: false},
	}
	planningTask := task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindOperationPlanningTask,
		Metadata: task.Metadata{ID: taskID, IdempotencyKey: runID + ":planning", CreatedAt: createdAt},
		Spec: task.Spec{NodeID: spec.NodeID, OperationID: spec.OperationID, OperationVersion: spec.OperationVersion,
			CapabilityDigest: spec.CapabilityDigest, Targets: spec.Targets, Parameters: spec.Parameters, SecretRefs: spec.SecretRefs},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: createdAt},
	}
	created, wasCreated, err := service.runs.CreateOperationRun(ctx, run, planningTask)
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return operationrun.Resource{}, false, &ConflictError{Err: fmt.Errorf("%w: %v", ErrOperationRunIdempotencyConflict, err)}
	}
	return created, wasCreated, err
}

func (service *OperationsService) GetOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return operationrun.Resource{}, &ValidationError{Err: fmt.Errorf("operation run id: %w", err)}
	}
	return service.runs.GetOperationRun(ctx, id)
}

func (service *OperationsService) ListOperationRuns(ctx context.Context, options protocol.ListOptions) ([]operationrun.Resource, protocol.ListOptions, error) {
	options = normalizeListOptions(options)
	runs, err := service.runs.ListOperationRuns(ctx, options.Limit, options.Offset)
	return runs, options, err
}

func (service *OperationsService) ConfirmOperationRun(ctx context.Context, id string, request protocol.ConfirmOperationRunRequest) (operationrun.Resource, error) {
	if err := validateIdentifier(strings.TrimSpace(request.IdempotencyKey)); err != nil {
		return operationrun.Resource{}, &ValidationError{Err: fmt.Errorf("idempotency_key: %w", err)}
	}
	run, err := service.GetOperationRun(ctx, id)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if run.PlanDigest == "" || strings.TrimSpace(request.PlanDigest) != run.PlanDigest {
		return operationrun.Resource{}, &ConflictError{Err: ErrOperationPlanDigestConflict}
	}
	if run.Status.State != operation.StateAwaitingConfirm {
		return operationrun.Resource{}, &ConflictError{Err: ErrOperationStateConflict}
	}
	return operationrun.Resource{}, &ConflictError{Err: ErrProductApplyDisabled}
}

func (service *OperationsService) CancelOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return operationrun.Resource{}, &ValidationError{Err: fmt.Errorf("operation run id: %w", err)}
	}
	run, err := service.runs.CancelOperationRun(ctx, id, service.now().UTC())
	return run, classifyTaskConflict(err)
}

func (service *OperationsService) validateCreateOperationRun(ctx context.Context, request protocol.CreateOperationRunRequest) (operationrun.Spec, string, operation.Metadata, error) {
	if request.APIVersion != "setpoint.io/v1" || request.Kind != "OperationRun" {
		return operationrun.Spec{}, "", operation.Metadata{}, errors.New("api_version and kind must identify a setpoint.io/v1 OperationRun")
	}
	key := strings.TrimSpace(request.Metadata.IdempotencyKey)
	if err := validateIdentifier(key); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, fmt.Errorf("metadata.idempotency_key: %w", err)
	}
	operationID := strings.TrimSpace(request.Spec.OperationID)
	if err := validateIdentifier(operationID); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, fmt.Errorf("spec.operation_id: %w", err)
	}
	metadata, ok := service.catalog.Get(operationID)
	if !ok {
		return operationrun.Spec{}, "", operation.Metadata{}, errors.New("spec.operation_id does not identify a registered operation")
	}
	digest, err := operation.CapabilityDigest(metadata)
	if err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, err
	}
	nodeID := strings.TrimSpace(request.Spec.NodeID)
	if err := validateIdentifier(nodeID); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, fmt.Errorf("spec.node_id: %w", err)
	}
	if _, err := service.nodes.GetNode(ctx, nodeID, service.offlineAfter); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, fmt.Errorf("spec.node_id: %w", err)
	}
	targets, err := normalizeOperationTargets(request.Spec.Targets, nodeID)
	if err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, err
	}
	parameters, canonical, err := canonicalParameters(request.Spec.Parameters)
	if err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, err
	}
	if err := rejectPlaintextSecrets(parameters, "spec.parameters"); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, err
	}
	if err := validateOperationParameters(metadata, parameters); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, err
	}
	normalized, err := service.catalog.NormalizeParameters(operationID, canonical)
	if err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, fmt.Errorf("spec.parameters: %w", err)
	}
	if _, normalized, err = canonicalParameters(normalized); err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, fmt.Errorf("spec.parameters: capability returned invalid normalized parameters")
	}
	secretRefs, err := normalizeSecretRefs(metadata, request.Spec.SecretRefs)
	if err != nil {
		return operationrun.Spec{}, "", operation.Metadata{}, err
	}
	return operationrun.Spec{OperationID: operationID, OperationVersion: metadata.Version, CapabilityDigest: digest,
		NodeID: nodeID, Targets: targets, Parameters: normalized, SecretRefs: secretRefs}, key, metadata, nil
}

func normalizeOperationTargets(targets []operation.Target, nodeID string) ([]operation.Target, error) {
	if len(targets) == 0 {
		return nil, errors.New("spec.targets must not be empty")
	}
	result := append([]operation.Target(nil), targets...)
	hasExecutionNode := false
	seen := map[string]struct{}{}
	for index, target := range result {
		if err := operation.ValidateTarget(target); err != nil {
			return nil, fmt.Errorf("spec.targets[%d]: %w", index, err)
		}
		encoded, _ := json.Marshal(target)
		if _, ok := seen[string(encoded)]; ok {
			return nil, errors.New("spec.targets contains a duplicate target")
		}
		seen[string(encoded)] = struct{}{}
		if target.Kind == operation.TargetNode && target.NodeID == nodeID {
			hasExecutionNode = true
		}
	}
	if !hasExecutionNode {
		return nil, errors.New("spec.targets must include the selected execution node")
	}
	return result, nil
}

func validateOperationParameters(metadata operation.Metadata, supplied map[string]json.RawMessage) error {
	declared := make(map[string]operation.Parameter, len(metadata.Parameters))
	for _, parameter := range metadata.Parameters {
		declared[parameter.Name] = parameter
	}
	for name, raw := range supplied {
		parameter, ok := declared[name]
		if !ok {
			return fmt.Errorf("spec.parameters contains undeclared parameter %q", name)
		}
		if err := validateOperationParameterType(parameter.Type, raw); err != nil {
			return fmt.Errorf("spec.parameters.%s: %w", name, err)
		}
	}
	for name, parameter := range declared {
		if parameter.Required {
			if _, ok := supplied[name]; !ok {
				return fmt.Errorf("spec.parameters.%s is required", name)
			}
		}
	}
	return nil
}

func validateOperationParameterType(kind string, raw json.RawMessage) error {
	switch kind {
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("must be a string")
		}
	case "string[]":
		var value []string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return errors.New("must be an array of strings")
		}
	case "object":
		var value map[string]json.RawMessage
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return errors.New("must be a JSON object")
		}
	default:
		return fmt.Errorf("uses unsupported declared type %q", kind)
	}
	return nil
}

func rejectPlaintextSecrets(values map[string]json.RawMessage, path string) error {
	for name, raw := range values {
		if sensitiveParameterName(name) {
			return fmt.Errorf("%s.%s must not contain plaintext secret material", path, name)
		}
		if err := rejectSecretJSON(raw, path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func rejectSecretJSON(raw json.RawMessage, path string) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		return rejectPlaintextSecrets(object, path)
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) == nil && array != nil {
		for index, item := range array {
			if err := rejectSecretJSON(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func sensitiveParameterName(name string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
	for _, marker := range []string{"password", "passwd", "secret", "token", "privatekey", "authorization", "credential", "apikey", "accesskey", "secretkey", "clientsecret", "bearer"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func normalizeSecretRefs(metadata operation.Metadata, refs []operation.SecretRef) ([]operation.SecretRef, error) {
	allowed := map[string]operation.SecretRequirement{}
	for _, requirement := range metadata.SecretRequirements {
		allowed[requirement.ID] = requirement
	}
	seen := map[string]struct{}{}
	result := make([]operation.SecretRef, 0, len(refs))
	for index, ref := range refs {
		ref.RequirementID = strings.TrimSpace(ref.RequirementID)
		ref.Reference = strings.TrimSpace(ref.Reference)
		if _, ok := allowed[ref.RequirementID]; !ok {
			return nil, fmt.Errorf("spec.secret_refs[%d] has an undeclared requirement_id", index)
		}
		if err := validateIdentifier(ref.Reference); err != nil {
			return nil, fmt.Errorf("spec.secret_refs[%d].reference: %w", index, err)
		}
		if _, ok := seen[ref.RequirementID]; ok {
			return nil, errors.New("spec.secret_refs contains duplicate requirement_id")
		}
		seen[ref.RequirementID] = struct{}{}
		result = append(result, ref)
	}
	for _, requirement := range metadata.SecretRequirements {
		if requirement.Required {
			if _, ok := seen[requirement.ID]; !ok {
				return nil, fmt.Errorf("spec.secret_refs requires %q", requirement.ID)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RequirementID < result[j].RequirementID })
	return result, nil
}
