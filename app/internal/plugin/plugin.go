package plugin

import (
	"errors"
	"fmt"
	"strings"
)

type Mode string

const (
	ModeReadOnly Mode = "read_only"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type CheckItemDefinition struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	RecommendedValue string   `json:"recommended_value"`
	SourceRefs       []string `json:"source_refs,omitempty"`
}
type Parameter struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Options     []string `json:"options,omitempty"`
}

type Metadata struct {
	ID               string                `json:"id"`
	Category         string                `json:"category"`
	Name             string                `json:"name"`
	Version          string                `json:"version"`
	Description      string                `json:"description"`
	Mode             Mode                  `json:"mode"`
	Risk             RiskLevel             `json:"risk"`
	Impact           string                `json:"impact"`
	SupportedSystems []string              `json:"supported_systems"`
	Parameters       []Parameter           `json:"parameters"`
	Checks           []CheckItemDefinition `json:"checks"`
}

// CheckDescriptor exposes immutable check metadata. Executable checks opt in
// through CheckDefinition.
type CheckDescriptor interface {
	Metadata() Metadata
}

func ValidateMetadata(metadata Metadata) error {
	if !validID(metadata.ID) {
		return errors.New("plugin ID must use lowercase letters, digits, '.', '_' or '-'")
	}
	if strings.TrimSpace(metadata.Name) == "" {
		return errors.New("plugin name is required")
	}
	if strings.TrimSpace(metadata.Category) == "" {
		return errors.New("plugin category is required")
	}
	if strings.TrimSpace(metadata.Version) == "" {
		return errors.New("plugin version is required")
	}
	if metadata.Mode != ModeReadOnly {
		return fmt.Errorf("unsupported plugin mode %q", metadata.Mode)
	}
	if !validRisk(metadata.Risk) {
		return fmt.Errorf("unsupported plugin risk %q", metadata.Risk)
	}
	if len(metadata.SupportedSystems) == 0 {
		return errors.New("at least one supported system is required")
	}
	if err := validateSupportedSystems(metadata.SupportedSystems); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(metadata.Parameters))
	for _, parameter := range metadata.Parameters {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return errors.New("plugin parameter name is required")
		}
		if name != parameter.Name {
			return fmt.Errorf("plugin parameter %q name must not contain surrounding whitespace", parameter.Name)
		}
		parameterType := strings.TrimSpace(parameter.Type)
		if parameterType == "" {
			return fmt.Errorf("plugin parameter %q type is required", name)
		}
		if parameterType != parameter.Type {
			return fmt.Errorf("plugin parameter %q type must not contain surrounding whitespace", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate plugin parameter %q", name)
		}
		seen[name] = struct{}{}
		if err := validateNonEmptyUniqueValues("plugin parameter "+name+" option", parameter.Options); err != nil {
			return err
		}
	}
	checkIDs := make(map[string]struct{}, len(metadata.Checks))
	for _, check := range metadata.Checks {
		if !validID(check.ID) || strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.RecommendedValue) == "" {
			return errors.New("plugin check definitions require a valid ID, name and recommended value")
		}
		if _, exists := checkIDs[check.ID]; exists {
			return fmt.Errorf("duplicate plugin check %q", check.ID)
		}
		checkIDs[check.ID] = struct{}{}
		if err := validateNonEmptyUniqueValues("plugin check "+check.ID+" source reference", check.SourceRefs); err != nil {
			return err
		}
	}
	return nil
}

func validateSupportedSystems(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return errors.New("supported system must not be empty")
		}
		if trimmed != value {
			return fmt.Errorf("supported system %q must not contain surrounding whitespace", value)
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate supported system %q", value)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateNonEmptyUniqueValues(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s must not be empty", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validRisk(risk RiskLevel) bool {
	switch risk {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	default:
		return false
	}
}

func cloneMetadata(metadata Metadata) Metadata {
	metadata.SupportedSystems = append([]string(nil), metadata.SupportedSystems...)
	metadata.Parameters = append([]Parameter(nil), metadata.Parameters...)
	metadata.Checks = append([]CheckItemDefinition(nil), metadata.Checks...)
	for index := range metadata.Parameters {
		metadata.Parameters[index].Options = append([]string(nil), metadata.Parameters[index].Options...)
	}
	for index := range metadata.Checks {
		metadata.Checks[index] = cloneCheckItemDefinition(metadata.Checks[index])
	}
	return metadata
}

func cloneCheckItemDefinition(definition CheckItemDefinition) CheckItemDefinition {
	definition.SourceRefs = append([]string(nil), definition.SourceRefs...)
	return definition
}
