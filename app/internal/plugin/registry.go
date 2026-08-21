package plugin

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	ErrDuplicate           = errors.New("check ID already registered")
	ErrDuplicateDefinition = errors.New("granular check ID already registered")
)

type CheckRegistry struct {
	mu          sync.RWMutex
	metadata    map[string]Metadata
	definitions map[string]CheckDefinition
	checkItems  map[string]CheckMetadata
	bundles     map[string]CheckBundle
	policies    map[string]CheckPolicy
}

func NewCheckRegistry() *CheckRegistry {
	return &CheckRegistry{
		metadata:    make(map[string]Metadata),
		definitions: make(map[string]CheckDefinition),
		checkItems:  make(map[string]CheckMetadata),
		bundles:     make(map[string]CheckBundle),
		policies:    make(map[string]CheckPolicy),
	}
}

func (registry *CheckRegistry) Register(candidate CheckDescriptor) error {
	if candidate == nil {
		return errors.New("check descriptor is required")
	}
	metadata := cloneMetadata(candidate.Metadata())
	if err := ValidateMetadata(metadata); err != nil {
		return fmt.Errorf("validate check metadata: %w", err)
	}

	items, bundle := metadataChecks(metadata)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.metadata[metadata.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, metadata.ID)
	}
	for id := range items {
		if _, exists := registry.checkItems[id]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateDefinition, id)
		}
	}
	registry.metadata[metadata.ID] = metadata
	for id, item := range items {
		registry.checkItems[id] = item
	}
	if len(bundle.CheckIDs) > 0 {
		registry.bundles[bundle.ID] = bundle
	}
	if executable, ok := candidate.(CheckDefinition); ok && metadata.Mode == ModeReadOnly {
		registry.definitions[metadata.ID] = executable
	}
	return nil
}

func (registry *CheckRegistry) Get(id string) (Metadata, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	metadata, exists := registry.metadata[id]
	if !exists {
		return Metadata{}, false
	}
	return cloneMetadata(metadata), true
}

func (registry *CheckRegistry) List() []Metadata {
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

func (registry *CheckRegistry) SupportsCheckExecution(id string) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	_, exists := registry.definitions[id]
	return exists
}

func (registry *CheckRegistry) Definition(id string) (CheckDefinition, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	candidate, exists := registry.definitions[id]
	return candidate, exists
}
