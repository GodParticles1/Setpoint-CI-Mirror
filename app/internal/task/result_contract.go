package task

import (
	"errors"
	"fmt"
	"strings"
)

// ResultContract is the immutable portion of registered check metadata needed
// to verify an Agent result without trusting the Agent as the source of truth.
type ResultContract struct {
	PluginID        string
	PluginVersion   string
	ItemIDs         []string
	ItemDefinitions []CheckDefinitionSnapshot
}

func ValidateCheckResult(result *CheckResult, contract ResultContract) error {
	if result == nil {
		return errors.New("check result is required")
	}
	if result.PluginID != contract.PluginID {
		return fmt.Errorf("result plugin_id %q does not match expected %q", result.PluginID, contract.PluginID)
	}
	if result.PluginVersion != contract.PluginVersion {
		return fmt.Errorf("result plugin_version %q does not match expected %q", result.PluginVersion, contract.PluginVersion)
	}
	if result.StartedAt.IsZero() || result.CompletedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) {
		return errors.New("result timestamps are invalid")
	}
	switch result.State {
	case CheckCompleted:
		if result.Error != nil {
			return errors.New("completed check result must not contain a top-level error")
		}
	case CheckError:
		if err := validateFailure(result.Error); err != nil {
			return fmt.Errorf("error check result: %w", err)
		}
	default:
		return fmt.Errorf("result state must be completed or error, got %q", result.State)
	}

	expected := make(map[string]struct{}, len(contract.ItemIDs))
	definitions := make(map[string]CheckDefinitionSnapshot, len(contract.ItemDefinitions))
	for _, definition := range contract.ItemDefinitions {
		definitions[definition.ID] = definition
	}
	for _, id := range contract.ItemIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return errors.New("result contract contains an empty item ID")
		}
		if _, exists := expected[id]; exists {
			return fmt.Errorf("result contract contains duplicate item ID %q", id)
		}
		expected[id] = struct{}{}
		if len(definitions) > 0 {
			if _, exists := definitions[id]; !exists {
				return fmt.Errorf("result contract is missing definition for item ID %q", id)
			}
		}
	}

	seen := make(map[string]struct{}, len(result.Items))
	for index := range result.Items {
		NormalizeItem(&result.Items[index])
		item := result.Items[index]
		if err := ValidateItem(item); err != nil {
			return fmt.Errorf("result.items[%d]: %w", index, err)
		}
		if item.Status == ItemError {
			if err := validateFailure(item.Error); err != nil {
				return fmt.Errorf("result.items[%d]: %w", index, err)
			}
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("result contains duplicate item ID %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if _, exists := expected[item.ID]; !exists {
			return fmt.Errorf("result contains unknown item ID %q", item.ID)
		}
		if definition, exists := definitions[item.ID]; exists &&
			(item.Name != definition.Name || item.RecommendedValue != definition.RecommendedValue) {
			return fmt.Errorf("result item %q does not match its frozen definition", item.ID)
		}
	}

	// An execution failure may stop after a verified subset has run. A normal
	// completion, including an all-not-applicable result, must return every item.
	if result.State == CheckCompleted {
		for _, id := range contract.ItemIDs {
			if _, exists := seen[id]; !exists {
				return fmt.Errorf("result is missing item ID %q", id)
			}
		}
	}
	return nil
}

func validateFailure(failure *Failure) error {
	if failure == nil {
		return errors.New("requires a top-level error")
	}
	if strings.TrimSpace(failure.Code) == "" || strings.TrimSpace(failure.Message) == "" {
		return errors.New("error code and message are required")
	}
	return nil
}
