package plugin

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

type CheckMetadata struct {
	ID               string      `json:"id"`
	PluginID         string      `json:"plugin_id"`
	PluginVersion    string      `json:"plugin_version"`
	Category         string      `json:"category"`
	Name             string      `json:"name"`
	Description      string      `json:"description"`
	RecommendedValue string      `json:"recommended_value"`
	Risk             RiskLevel   `json:"risk"`
	SupportedSystems []string    `json:"supported_systems"`
	Parameters       []Parameter `json:"parameters"`
	SourceRefs       []string    `json:"source_refs,omitempty"`
}

type CheckBundle struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	CheckIDs    []string `json:"check_ids"`
}

type CheckPolicy struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	CheckIDs    []string `json:"check_ids,omitempty"`
	BundleIDs   []string `json:"bundle_ids,omitempty"`
}

type ResolvedCheckGroup struct {
	PluginID string   `json:"plugin_id"`
	CheckIDs []string `json:"check_ids"`
}

type ResolvedCheckSelection struct {
	CheckIDs  []string             `json:"check_ids"`
	BundleIDs []string             `json:"bundle_ids,omitempty"`
	PolicyIDs []string             `json:"policy_ids,omitempty"`
	Groups    []ResolvedCheckGroup `json:"groups"`
}

func (registry *CheckRegistry) GetDefinition(id string) (CheckMetadata, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	metadata, exists := registry.checkItems[id]
	if !exists {
		return CheckMetadata{}, false
	}
	return cloneCheckMetadata(metadata), true
}

func (registry *CheckRegistry) ListDefinitions() []CheckMetadata {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]CheckMetadata, 0, len(registry.checkItems))
	for _, metadata := range registry.checkItems {
		result = append(result, cloneCheckMetadata(metadata))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func (registry *CheckRegistry) GetBundle(id string) (CheckBundle, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	bundle, exists := registry.bundles[id]
	if !exists {
		return CheckBundle{}, false
	}
	return cloneCheckBundle(bundle), true
}

func (registry *CheckRegistry) ListBundles() []CheckBundle {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]CheckBundle, 0, len(registry.bundles))
	for _, bundle := range registry.bundles {
		result = append(result, cloneCheckBundle(bundle))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func (registry *CheckRegistry) RegisterPolicy(policy CheckPolicy) error {
	policy = cloneCheckPolicy(policy)
	if !validID(policy.ID) || strings.TrimSpace(policy.Name) == "" || len(policy.CheckIDs)+len(policy.BundleIDs) == 0 {
		return errors.New("check policy requires a valid ID, name and at least one check or bundle")
	}
	policy.CheckIDs = normalizedCatalogIDs(policy.CheckIDs)
	policy.BundleIDs = normalizedCatalogIDs(policy.BundleIDs)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.policies[policy.ID]; exists {
		return fmt.Errorf("%w: policy %s", ErrDuplicate, policy.ID)
	}
	for _, id := range policy.CheckIDs {
		if _, exists := registry.checkItems[id]; !exists {
			return fmt.Errorf("policy %s references unknown check %q", policy.ID, id)
		}
	}
	for _, id := range policy.BundleIDs {
		if _, exists := registry.bundles[id]; !exists {
			return fmt.Errorf("policy %s references unknown bundle %q", policy.ID, id)
		}
	}
	registry.policies[policy.ID] = policy
	return nil
}

func (registry *CheckRegistry) GetPolicy(id string) (CheckPolicy, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	policy, exists := registry.policies[id]
	if !exists {
		return CheckPolicy{}, false
	}
	return cloneCheckPolicy(policy), true
}

func (registry *CheckRegistry) ListPolicies() []CheckPolicy {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]CheckPolicy, 0, len(registry.policies))
	for _, policy := range registry.policies {
		result = append(result, cloneCheckPolicy(policy))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func (registry *CheckRegistry) ResolveSelection(
	checkIDs, bundleIDs, policyIDs []string,
) (ResolvedCheckSelection, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	requestedChecks := normalizedCatalogIDs(checkIDs)
	requestedBundles := normalizedCatalogIDs(bundleIDs)
	requestedPolicies := normalizedCatalogIDs(policyIDs)
	if len(requestedChecks)+len(requestedBundles)+len(requestedPolicies) == 0 {
		return ResolvedCheckSelection{}, errors.New("at least one check, bundle or policy is required")
	}
	selected := make(map[string]struct{})
	addBundle := func(id string) error {
		bundle, exists := registry.bundles[id]
		if !exists {
			return fmt.Errorf("unknown check bundle %q", id)
		}
		for _, checkID := range bundle.CheckIDs {
			selected[checkID] = struct{}{}
		}
		return nil
	}
	for _, id := range requestedChecks {
		if _, exists := registry.checkItems[id]; exists {
			selected[id] = struct{}{}
			continue
		}
		// Before granular checks, check_ids carried plugin IDs. Accepting an
		// existing bundle here keeps old clients explicit and deterministic.
		if err := addBundle(id); err != nil {
			return ResolvedCheckSelection{}, fmt.Errorf("unknown check or legacy bundle %q", id)
		}
	}
	for _, id := range requestedBundles {
		if err := addBundle(id); err != nil {
			return ResolvedCheckSelection{}, err
		}
	}
	for _, id := range requestedPolicies {
		policy, exists := registry.policies[id]
		if !exists {
			return ResolvedCheckSelection{}, fmt.Errorf("unknown check policy %q", id)
		}
		for _, checkID := range policy.CheckIDs {
			selected[checkID] = struct{}{}
		}
		for _, bundleID := range policy.BundleIDs {
			if err := addBundle(bundleID); err != nil {
				return ResolvedCheckSelection{}, err
			}
		}
	}
	resolvedIDs := make([]string, 0, len(selected))
	groups := make(map[string][]string)
	for id := range selected {
		resolvedIDs = append(resolvedIDs, id)
		groups[registry.checkItems[id].PluginID] = append(groups[registry.checkItems[id].PluginID], id)
	}
	sort.Strings(resolvedIDs)
	pluginIDs := make([]string, 0, len(groups))
	for pluginID := range groups {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)
	resolvedGroups := make([]ResolvedCheckGroup, 0, len(pluginIDs))
	for _, pluginID := range pluginIDs {
		ids := groups[pluginID]
		sort.Strings(ids)
		resolvedGroups = append(resolvedGroups, ResolvedCheckGroup{PluginID: pluginID, CheckIDs: ids})
	}
	return ResolvedCheckSelection{
		CheckIDs: resolvedIDs, BundleIDs: requestedBundles, PolicyIDs: requestedPolicies, Groups: resolvedGroups,
	}, nil
}

func metadataChecks(metadata Metadata) (map[string]CheckMetadata, CheckBundle) {
	checks := make(map[string]CheckMetadata, len(metadata.Checks))
	ids := make([]string, 0, len(metadata.Checks))
	for _, definition := range metadata.Checks {
		checks[definition.ID] = CheckMetadata{
			ID: definition.ID, PluginID: metadata.ID, PluginVersion: metadata.Version,
			Category: metadata.Category, Name: definition.Name, Description: definition.Description,
			RecommendedValue: definition.RecommendedValue, Risk: metadata.Risk,
			SupportedSystems: append([]string(nil), metadata.SupportedSystems...),
			Parameters:       append([]Parameter(nil), metadata.Parameters...),
			SourceRefs:       append([]string(nil), definition.SourceRefs...),
		}
		ids = append(ids, definition.ID)
	}
	sort.Strings(ids)
	return checks, CheckBundle{
		ID: metadata.ID, Name: metadata.Name, Description: metadata.Description, Category: metadata.Category, CheckIDs: ids,
	}
}

func cloneCheckMetadata(metadata CheckMetadata) CheckMetadata {
	metadata.SupportedSystems = append([]string(nil), metadata.SupportedSystems...)
	metadata.Parameters = append([]Parameter(nil), metadata.Parameters...)
	for index := range metadata.Parameters {
		metadata.Parameters[index].Options = append([]string(nil), metadata.Parameters[index].Options...)
	}
	metadata.SourceRefs = append([]string(nil), metadata.SourceRefs...)
	return metadata
}

func cloneCheckBundle(bundle CheckBundle) CheckBundle {
	bundle.CheckIDs = append([]string(nil), bundle.CheckIDs...)
	return bundle
}

func cloneCheckPolicy(policy CheckPolicy) CheckPolicy {
	policy.CheckIDs = append([]string(nil), policy.CheckIDs...)
	policy.BundleIDs = append([]string(nil), policy.BundleIDs...)
	return policy
}

func normalizedCatalogIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
