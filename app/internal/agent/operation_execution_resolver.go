package agent

import (
	"context"
	"errors"
	"strings"

	"setpoint/internal/operation"
	"setpoint/internal/task"
)

type ResolvedOperationExecution struct {
	OperationID     string
	Definition      operation.OperationDefinition
	RestoreProvider operation.RestorePointProvider
}

type OperationExecutionAdapter interface {
	OperationID() string
	Resolve(context.Context, task.Resource) (ResolvedOperationExecution, error)
}

type OperationExecutionResolver struct {
	adapters map[string]OperationExecutionAdapter
}

func NewOperationExecutionResolver(adapters ...OperationExecutionAdapter) (*OperationExecutionResolver, error) {
	resolver := &OperationExecutionResolver{adapters: make(map[string]OperationExecutionAdapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			return nil, errors.New("operation execution adapter is required")
		}
		operationID := strings.TrimSpace(adapter.OperationID())
		if operationID == "" {
			return nil, errors.New("operation execution adapter ID is required")
		}
		if _, duplicate := resolver.adapters[operationID]; duplicate {
			return nil, errors.New("duplicate operation execution adapter " + operationID)
		}
		resolver.adapters[operationID] = adapter
	}
	return resolver, nil
}

func (resolver *OperationExecutionResolver) Resolve(ctx context.Context, resource task.Resource) (ResolvedOperationExecution, error) {
	if resolver == nil || resource.Spec.OperationExecution == nil {
		return ResolvedOperationExecution{}, errors.New("operation execution resolver and frozen contract are required")
	}
	operationID := strings.TrimSpace(resource.Spec.OperationExecution.OperationID)
	adapter, ok := resolver.adapters[operationID]
	if !ok {
		return ResolvedOperationExecution{}, errors.New("operation execution capability is unavailable")
	}
	resolved, err := adapter.Resolve(ctx, resource)
	if err != nil {
		return ResolvedOperationExecution{}, err
	}
	if resolved.OperationID != operationID || resolved.Definition == nil || resolved.RestoreProvider == nil {
		return ResolvedOperationExecution{}, errors.New("operation execution adapter returned a mismatched or incomplete capability")
	}
	return resolved, nil
}

type staticOperationExecutionAdapter struct {
	operationID string
	definition  operation.OperationDefinition
	restore     operation.RestorePointProvider
}

func NewStaticOperationExecutionAdapter(operationID string, definition operation.OperationDefinition, restore operation.RestorePointProvider) (OperationExecutionAdapter, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" || definition == nil || restore == nil {
		return nil, errors.New("static operation ID, definition and restore provider are required")
	}
	if definition.Metadata().ID != operationID {
		return nil, errors.New("static operation definition ID does not match adapter ID")
	}
	return &staticOperationExecutionAdapter{operationID: operationID, definition: definition, restore: restore}, nil
}

func (adapter *staticOperationExecutionAdapter) OperationID() string { return adapter.operationID }

func (adapter *staticOperationExecutionAdapter) Resolve(context.Context, task.Resource) (ResolvedOperationExecution, error) {
	return ResolvedOperationExecution{OperationID: adapter.operationID, Definition: adapter.definition, RestoreProvider: adapter.restore}, nil
}
