package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"setpoint/internal/trustedexec"
)

const CheckExecutionContractVersion = 2

type CheckDefinitionSnapshot struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	RecommendedValue string   `json:"recommended_value"`
	SourceRefs       []string `json:"source_refs,omitempty"`
}

type CheckExecutionContract struct {
	SchemaVersion          int                       `json:"schema_version"`
	PluginID               string                    `json:"plugin_id"`
	PluginVersion          string                    `json:"plugin_version"`
	Parameters             json.RawMessage           `json:"parameters"`
	Checks                 []CheckDefinitionSnapshot `json:"checks"`
	TrustedExecutableRoots []trustedexec.Root        `json:"trusted_executable_roots,omitempty"`
}

func NewCheckExecutionContract(
	pluginID, pluginVersion string,
	parameters json.RawMessage,
	checks []CheckDefinitionSnapshot,
	trustedRoots ...[]trustedexec.Root,
) (CheckExecutionContract, string, error) {
	canonicalParameters, err := canonicalContractParameters(parameters)
	if err != nil {
		return CheckExecutionContract{}, "", err
	}
	var roots []trustedexec.Root
	if len(trustedRoots) > 1 {
		return CheckExecutionContract{}, "", errors.New("check execution contract accepts at most one trusted root set")
	}
	if len(trustedRoots) == 1 {
		roots, err = trustedexec.CanonicalRoots(trustedRoots[0])
		if err != nil {
			return CheckExecutionContract{}, "", err
		}
	}
	contract := CheckExecutionContract{
		SchemaVersion:          CheckExecutionContractVersion,
		PluginID:               strings.TrimSpace(pluginID),
		PluginVersion:          strings.TrimSpace(pluginVersion),
		Parameters:             canonicalParameters,
		Checks:                 cloneCheckSnapshots(checks),
		TrustedExecutableRoots: roots,
	}
	sort.Slice(contract.Checks, func(left, right int) bool {
		return contract.Checks[left].ID < contract.Checks[right].ID
	})
	for index := range contract.Checks {
		contract.Checks[index].SourceRefs = normalizedSourceRefs(contract.Checks[index].SourceRefs)
	}
	if err := validateCheckExecutionContract(contract); err != nil {
		return CheckExecutionContract{}, "", err
	}
	digest, err := checkExecutionContractDigest(contract)
	if err != nil {
		return CheckExecutionContract{}, "", err
	}
	return contract, digest, nil
}

func ValidateCheckExecutionContract(contract CheckExecutionContract, digest string) error {
	if err := validateCheckExecutionContract(contract); err != nil {
		return err
	}
	actual, err := checkExecutionContractDigest(contract)
	if err != nil {
		return err
	}
	if strings.TrimSpace(digest) == "" || !strings.EqualFold(actual, strings.TrimSpace(digest)) {
		return errors.New("check execution contract digest does not match its frozen content")
	}
	return nil
}

func (contract CheckExecutionContract) ResultContract() ResultContract {
	itemIDs := make([]string, 0, len(contract.Checks))
	for _, definition := range contract.Checks {
		itemIDs = append(itemIDs, definition.ID)
	}
	return ResultContract{
		PluginID: contract.PluginID, PluginVersion: contract.PluginVersion, ItemIDs: itemIDs,
		ItemDefinitions: cloneCheckSnapshots(contract.Checks),
	}
}

func CloneCheckExecutionContract(contract *CheckExecutionContract) *CheckExecutionContract {
	if contract == nil {
		return nil
	}
	copy := *contract
	copy.Parameters = append(json.RawMessage(nil), contract.Parameters...)
	copy.Checks = cloneCheckSnapshots(contract.Checks)
	copy.TrustedExecutableRoots = append([]trustedexec.Root(nil), contract.TrustedExecutableRoots...)
	return &copy
}

func validateCheckExecutionContract(contract CheckExecutionContract) error {
	if contract.SchemaVersion != 1 && contract.SchemaVersion != CheckExecutionContractVersion {
		return fmt.Errorf("unsupported check execution contract schema version %d", contract.SchemaVersion)
	}
	if contract.SchemaVersion == 1 && len(contract.TrustedExecutableRoots) > 0 {
		return errors.New("check execution contract v1 cannot contain trusted executable roots")
	}
	if !validContractIdentifier(contract.PluginID) || strings.TrimSpace(contract.PluginVersion) == "" {
		return errors.New("check execution contract requires a valid plugin ID and version")
	}
	canonicalParameters, err := canonicalContractParameters(contract.Parameters)
	if err != nil {
		return err
	}
	if string(canonicalParameters) != string(contract.Parameters) {
		return errors.New("check execution contract parameters are not canonical")
	}
	if len(contract.Checks) == 0 {
		return errors.New("check execution contract requires at least one check")
	}
	canonicalRoots, err := trustedexec.CanonicalRoots(contract.TrustedExecutableRoots)
	if err != nil {
		return fmt.Errorf("check execution contract trusted executable roots: %w", err)
	}
	if !equalTrustedRoots(contract.TrustedExecutableRoots, canonicalRoots) {
		return errors.New("check execution contract trusted executable roots are not canonical")
	}
	seen := make(map[string]struct{}, len(contract.Checks))
	previous := ""
	for _, definition := range contract.Checks {
		if !validContractIdentifier(definition.ID) || strings.TrimSpace(definition.Name) == "" ||
			strings.TrimSpace(definition.RecommendedValue) == "" {
			return errors.New("check execution contract contains an invalid check definition")
		}
		if _, exists := seen[definition.ID]; exists {
			return fmt.Errorf("check execution contract contains duplicate check %q", definition.ID)
		}
		seen[definition.ID] = struct{}{}
		if previous != "" && previous > definition.ID {
			return errors.New("check execution contract checks are not in canonical order")
		}
		previous = definition.ID
		if !equalStringSlices(definition.SourceRefs, normalizedSourceRefs(definition.SourceRefs)) {
			return fmt.Errorf("check execution contract source refs for %q are not canonical", definition.ID)
		}
	}
	return nil
}

func checkExecutionContractDigest(contract CheckExecutionContract) (string, error) {
	encoded, err := json.Marshal(contract)
	if err != nil {
		return "", fmt.Errorf("encode check execution contract: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func CheckExecutionContractDigest(contract CheckExecutionContract) (string, error) {
	return checkExecutionContractDigest(contract)
}

func canonicalContractParameters(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var parameters map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parameters); err != nil || parameters == nil {
		return nil, errors.New("check execution contract parameters must be a JSON object")
	}
	canonical, err := json.Marshal(parameters)
	if err != nil {
		return nil, fmt.Errorf("canonicalize check execution contract parameters: %w", err)
	}
	return canonical, nil
}

func cloneCheckSnapshots(checks []CheckDefinitionSnapshot) []CheckDefinitionSnapshot {
	result := append([]CheckDefinitionSnapshot(nil), checks...)
	for index := range result {
		result[index].SourceRefs = append([]string(nil), result[index].SourceRefs...)
	}
	return result
}

func normalizedSourceRefs(values []string) []string {
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

func validContractIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalTrustedRoots(left, right []trustedexec.Root) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
