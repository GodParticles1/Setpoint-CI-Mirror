package operation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const digestPrefix = "sha256:"

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type TargetKind string

const (
	TargetSite       TargetKind = "site"
	TargetNode       TargetKind = "node"
	TargetComponent  TargetKind = "component"
	TargetDataObject TargetKind = "data_object"
)

type Target struct {
	Kind      TargetKind `json:"kind"`
	SiteID    string     `json:"site_id,omitempty"`
	NodeID    string     `json:"node_id,omitempty"`
	Component string     `json:"component,omitempty"`
	Resource  string     `json:"resource,omitempty"`
}

type Parameter struct {
	Name        string           `json:"name"`
	Type        string           `json:"type"`
	Description string           `json:"description"`
	Required    bool             `json:"required"`
	Options     []string         `json:"options,omitempty"`
	Fields      []ParameterField `json:"fields,omitempty"`
}

// ParameterField describes one bounded object member for catalog-driven
// clients. Nested objects are deliberately unsupported; capability-specific
// normalization remains the authoritative Server-side validator.
type ParameterField struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Options     []string `json:"options,omitempty"`
}

type SecretRequirement struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type Metadata struct {
	ID                 string              `json:"id"`
	Category           string              `json:"category"`
	Name               string              `json:"name"`
	Version            string              `json:"version"`
	Description        string              `json:"description"`
	Risk               RiskLevel           `json:"risk"`
	Impact             string              `json:"impact"`
	SupportedSystems   []string            `json:"supported_systems"`
	Parameters         []Parameter         `json:"parameters,omitempty"`
	SecretRequirements []SecretRequirement `json:"secret_requirements,omitempty"`
}

type Descriptor interface {
	Metadata() Metadata
}

// ParameterNormalizer lets a compile-time capability validate and canonicalize
// its declared structured parameter objects before the Server persists a run.
// It carries no execution behavior and keeps capability-specific fields out of
// HTTP handlers.
type ParameterNormalizer interface {
	NormalizeParameters(json.RawMessage) (json.RawMessage, error)
}

type MetadataDescriptor struct {
	metadata Metadata
}

func NewMetadataDescriptor(metadata Metadata) Descriptor {
	return MetadataDescriptor{metadata: cloneMetadata(metadata)}
}

func (descriptor MetadataDescriptor) Metadata() Metadata {
	return cloneMetadata(descriptor.metadata)
}

func ValidateMetadata(metadata Metadata) error {
	if !validID(metadata.ID) {
		return errors.New("operation ID must use lowercase letters, digits, '.', '_' or '-'")
	}
	if strings.TrimSpace(metadata.Name) == "" {
		return errors.New("operation name is required")
	}
	if strings.TrimSpace(metadata.Category) == "" {
		return errors.New("operation category is required")
	}
	if strings.TrimSpace(metadata.Version) == "" {
		return errors.New("operation version is required")
	}
	if !validRisk(metadata.Risk) {
		return fmt.Errorf("unsupported operation risk %q", metadata.Risk)
	}
	if len(metadata.SupportedSystems) == 0 {
		return errors.New("at least one supported system is required")
	}

	parameterNames := make(map[string]struct{}, len(metadata.Parameters))
	for _, parameter := range metadata.Parameters {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return errors.New("operation parameter name is required")
		}
		if strings.TrimSpace(parameter.Type) == "" {
			return fmt.Errorf("operation parameter %q requires a type", name)
		}
		if !validParameterType(parameter.Type) {
			return fmt.Errorf("operation parameter %q uses unsupported type %q", name, parameter.Type)
		}
		if _, exists := parameterNames[name]; exists {
			return fmt.Errorf("duplicate operation parameter %q", name)
		}
		parameterNames[name] = struct{}{}
		if err := validateParameterFields(name, parameter); err != nil {
			return err
		}
	}

	secretIDs := make(map[string]struct{}, len(metadata.SecretRequirements))
	for _, requirement := range metadata.SecretRequirements {
		if !validID(requirement.ID) {
			return errors.New("operation secret requirement requires a valid ID")
		}
		if _, exists := secretIDs[requirement.ID]; exists {
			return fmt.Errorf("duplicate operation secret requirement %q", requirement.ID)
		}
		secretIDs[requirement.ID] = struct{}{}
	}
	return nil
}

func ValidateTarget(target Target) error {
	switch target.Kind {
	case TargetSite:
		if strings.TrimSpace(target.SiteID) == "" {
			return errors.New("site target requires site_id")
		}
	case TargetNode:
		if strings.TrimSpace(target.NodeID) == "" {
			return errors.New("node target requires node_id")
		}
	case TargetComponent:
		if strings.TrimSpace(target.Component) == "" {
			return errors.New("component target requires component")
		}
		if strings.TrimSpace(target.SiteID) == "" && strings.TrimSpace(target.NodeID) == "" {
			return errors.New("component target requires site_id or node_id")
		}
	case TargetDataObject:
		if strings.TrimSpace(target.Component) == "" || strings.TrimSpace(target.Resource) == "" {
			return errors.New("data object target requires component and resource")
		}
	default:
		return fmt.Errorf("unsupported target kind %q", target.Kind)
	}
	return nil
}

func CapabilityDigest(metadata Metadata) (string, error) {
	if err := ValidateMetadata(metadata); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cloneMetadata(metadata))
	if err != nil {
		return "", fmt.Errorf("encode operation metadata digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return digestPrefix + hex.EncodeToString(digest[:]), nil
}

func cloneMetadata(metadata Metadata) Metadata {
	metadata.SupportedSystems = append([]string(nil), metadata.SupportedSystems...)
	metadata.Parameters = append([]Parameter(nil), metadata.Parameters...)
	metadata.SecretRequirements = append([]SecretRequirement(nil), metadata.SecretRequirements...)
	for index := range metadata.Parameters {
		metadata.Parameters[index].Options = append([]string(nil), metadata.Parameters[index].Options...)
		metadata.Parameters[index].Fields = append([]ParameterField(nil), metadata.Parameters[index].Fields...)
		for fieldIndex := range metadata.Parameters[index].Fields {
			metadata.Parameters[index].Fields[fieldIndex].Options = append([]string(nil), metadata.Parameters[index].Fields[fieldIndex].Options...)
		}
	}
	return metadata
}

func validParameterType(kind string) bool {
	switch kind {
	case "string", "string[]", "object":
		return true
	default:
		return false
	}
}

func validateParameterFields(parameterName string, parameter Parameter) error {
	if parameter.Type != "object" {
		if len(parameter.Fields) != 0 {
			return fmt.Errorf("operation parameter %q declares fields for non-object type", parameterName)
		}
		return nil
	}
	if len(parameter.Fields) == 0 {
		return fmt.Errorf("operation object parameter %q requires declared fields", parameterName)
	}
	fieldNames := make(map[string]struct{}, len(parameter.Fields))
	for _, field := range parameter.Fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			return fmt.Errorf("operation object parameter %q has a field without a name", parameterName)
		}
		switch field.Type {
		case "string", "integer", "boolean", "string[]":
		default:
			return fmt.Errorf("operation object parameter %q field %q uses unsupported type %q", parameterName, name, field.Type)
		}
		if _, exists := fieldNames[name]; exists {
			return fmt.Errorf("operation object parameter %q has duplicate field %q", parameterName, name)
		}
		fieldNames[name] = struct{}{}
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
