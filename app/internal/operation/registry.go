package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var ErrDuplicateOperation = errors.New("operation ID already registered")

type Registry struct {
	mu          sync.RWMutex
	metadata    map[string]Metadata
	planning    map[string]PlanningDefinition
	definitions map[string]OperationDefinition
	normalizers map[string]ParameterNormalizer
}

func NewRegistry() *Registry {
	return &Registry{
		metadata:    make(map[string]Metadata),
		planning:    make(map[string]PlanningDefinition),
		definitions: make(map[string]OperationDefinition),
		normalizers: make(map[string]ParameterNormalizer),
	}
}

func (registry *Registry) Register(candidate Descriptor) error {
	if candidate == nil {
		return errors.New("operation descriptor is required")
	}
	metadata := cloneMetadata(candidate.Metadata())
	if err := ValidateMetadata(metadata); err != nil {
		return fmt.Errorf("validate operation metadata: %w", err)
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.metadata[metadata.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateOperation, metadata.ID)
	}
	registry.metadata[metadata.ID] = metadata
	if definition, ok := candidate.(PlanningDefinition); ok {
		registry.planning[metadata.ID] = definition
	}
	if definition, ok := candidate.(OperationDefinition); ok {
		registry.definitions[metadata.ID] = definition
	}
	if normalizer, ok := candidate.(ParameterNormalizer); ok {
		registry.normalizers[metadata.ID] = normalizer
	}
	return nil
}

func (registry *Registry) NormalizeParameters(id string, parameters json.RawMessage) (json.RawMessage, error) {
	registry.mu.RLock()
	normalizer := registry.normalizers[id]
	metadata, exists := registry.metadata[id]
	registry.mu.RUnlock()
	if normalizer == nil {
		if exists {
			for _, parameter := range metadata.Parameters {
				if parameter.Type == "object" {
					return nil, fmt.Errorf("operation %q requires a structured parameter normalizer", id)
				}
			}
		}
		return append(json.RawMessage(nil), parameters...), nil
	}
	return normalizer.NormalizeParameters(parameters)
}

func (registry *Registry) PlanningDefinition(id string) (PlanningDefinition, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	definition, exists := registry.planning[id]
	return definition, exists
}

func (registry *Registry) Get(id string) (Metadata, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	metadata, exists := registry.metadata[id]
	if !exists {
		return Metadata{}, false
	}
	return cloneMetadata(metadata), true
}

func (registry *Registry) List() []Metadata {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]Metadata, 0, len(registry.metadata))
	for _, metadata := range registry.metadata {
		result = append(result, cloneMetadata(metadata))
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ID < result[right].ID
	})
	return result
}

func (registry *Registry) Definition(id string) (OperationDefinition, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	definition, exists := registry.definitions[id]
	return definition, exists
}
