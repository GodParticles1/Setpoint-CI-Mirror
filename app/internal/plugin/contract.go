package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

var ErrCheckContractMismatch = errors.New("frozen check execution contract does not match the registered definition")

func FreezeExecutionContract(
	metadata Metadata,
	selectedCheckIDs []string,
	parameters json.RawMessage,
	trustedRoots ...[]trustedexec.Root,
) (task.CheckExecutionContract, string, error) {
	if err := ValidateMetadata(metadata); err != nil {
		return task.CheckExecutionContract{}, "", fmt.Errorf("validate check metadata: %w", err)
	}
	selected, err := selectedDefinitions(metadata, selectedCheckIDs)
	if err != nil {
		return task.CheckExecutionContract{}, "", err
	}
	snapshots := make([]task.CheckDefinitionSnapshot, 0, len(selected))
	for _, definition := range selected {
		snapshots = append(snapshots, task.CheckDefinitionSnapshot{
			ID: definition.ID, Name: definition.Name, Description: definition.Description,
			RecommendedValue: definition.RecommendedValue, SourceRefs: append([]string(nil), definition.SourceRefs...),
		})
	}
	return task.NewCheckExecutionContract(metadata.ID, metadata.Version, parameters, snapshots, trustedRoots...)
}

func MetadataForExecutionContract(
	registry *CheckRegistry,
	contract task.CheckExecutionContract,
	digest string,
) (Metadata, error) {
	if registry == nil {
		return Metadata{}, errors.New("check registry is required")
	}
	if err := task.ValidateCheckExecutionContract(contract, digest); err != nil {
		return Metadata{}, err
	}
	metadata, exists := registry.Get(contract.PluginID)
	if !exists || metadata.Version != contract.PluginVersion {
		return Metadata{}, fmt.Errorf("%w: %s@%s", ErrCheckContractMismatch, contract.PluginID, contract.PluginVersion)
	}
	selectedIDs := make([]string, 0, len(contract.Checks))
	for _, definition := range contract.Checks {
		selectedIDs = append(selectedIDs, definition.ID)
	}
	current, _, err := FreezeExecutionContract(
		metadata, selectedIDs, contract.Parameters, contract.TrustedExecutableRoots,
	)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrCheckContractMismatch, err)
	}
	current.SchemaVersion = contract.SchemaVersion
	currentDigest, err := task.CheckExecutionContractDigest(current)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrCheckContractMismatch, err)
	}
	if currentDigest != digest {
		return Metadata{}, fmt.Errorf("%w: %s@%s metadata changed without a version change", ErrCheckContractMismatch, contract.PluginID, contract.PluginVersion)
	}
	metadata.Checks, err = selectedDefinitions(metadata, selectedIDs)
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %v", ErrCheckContractMismatch, err)
	}
	return metadata, nil
}

func selectedDefinitions(metadata Metadata, selectedCheckIDs []string) ([]CheckItemDefinition, error) {
	available := make(map[string]CheckItemDefinition, len(metadata.Checks))
	for _, definition := range metadata.Checks {
		available[definition.ID] = definition
	}
	if len(selectedCheckIDs) == 0 {
		selectedCheckIDs = make([]string, 0, len(metadata.Checks))
		for _, definition := range metadata.Checks {
			selectedCheckIDs = append(selectedCheckIDs, definition.ID)
		}
	}
	seen := make(map[string]struct{}, len(selectedCheckIDs))
	result := make([]CheckItemDefinition, 0, len(selectedCheckIDs))
	for _, id := range selectedCheckIDs {
		id = strings.TrimSpace(id)
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		definition, exists := available[id]
		if !exists {
			return nil, fmt.Errorf("selected check %q is not provided by %s", id, metadata.ID)
		}
		seen[id] = struct{}{}
		result = append(result, cloneCheckItemDefinition(definition))
	}
	if len(result) == 0 {
		return nil, errors.New("at least one check must be selected")
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}
