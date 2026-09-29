package scriptcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

const (
	SchemaVersion = "setpoint.script-check/v1"
	outputLimit    = 256 << 10
)

var sensitiveParameterMarkers = []string{
	"password", "passwd", "secret", "token", "privatekey", "authorization",
	"credential", "apikey", "accesskey", "secretkey", "clientsecret", "bearer",
}

// Definition adapts a standalone observation-only executable to Setpoint's
// immutable CheckDefinition contract. The executable remains independently
// runnable; Setpoint invokes only its explicit JSON protocol mode.
type Definition struct {
	descriptor plugin.MetadataDescriptor
	command    string
	baseArgs   []string
}

func New(metadata plugin.Metadata, command string, baseArgs ...string) (*Definition, error) {
	if err := plugin.ValidateMetadata(metadata); err != nil {
		return nil, fmt.Errorf("validate script check metadata: %w", err)
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("script check command is required")
	}
	if strings.ContainsRune(command, 0) {
		return nil, errors.New("script check command contains NUL")
	}
	for _, parameter := range metadata.Parameters {
		if sensitiveParameterName(parameter.Name) {
			return nil, fmt.Errorf("script check parameter %q is secret-like and cannot be passed on argv", parameter.Name)
		}
	}
	args := append([]string(nil), baseArgs...)
	for _, argument := range args {
		if strings.ContainsRune(argument, 0) {
			return nil, errors.New("script check base argument contains NUL")
		}
	}
	return &Definition{
		descriptor: plugin.NewMetadataDescriptor(metadata),
		command:    command,
		baseArgs:   args,
	}, nil
}

func (definition *Definition) Metadata() plugin.Metadata {
	return definition.descriptor.Metadata()
}

// Detection is deliberately side-effect free and does not execute the script.
// Component applicability belongs in the script report as not_applicable items.
func (*Definition) Detect(context.Context, plugin.CheckInput) (plugin.Detection, error) {
	return plugin.Detection{Applicable: true}, nil
}

func (definition *Definition) Check(ctx context.Context, input plugin.CheckInput) ([]task.CheckItem, error) {
	if input.Executor == nil {
		return nil, errors.New("script check executor is required")
	}
	parameters, err := canonicalParameters(input.Parameters)
	if err != nil {
		return nil, err
	}
	selected, err := json.Marshal(input.SelectedCheckIDs)
	if err != nil {
		return nil, fmt.Errorf("encode selected check IDs: %w", err)
	}

	args := append([]string(nil), definition.baseArgs...)
	args = append(args,
		"--setpoint-json-v1",
		"--setpoint-parameters-json", parameters,
		"--setpoint-check-ids-json", string(selected),
	)
	result, err := input.Executor.Execute(ctx, executor.Command{
		Name: definition.command, Args: args, OutputLimit: outputLimit,
	})
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return nil, errors.New("script check output exceeded the bounded executor limit")
	}
	report, err := decodeReport(result.Stdout)
	if err != nil {
		return nil, err
	}
	return definition.items(report)
}

type report struct {
	SchemaVersion string       `json:"schema_version"`
	Items         []reportItem `json:"items"`
}

type reportItem struct {
	ID              string          `json:"id"`
	Status          task.ItemStatus `json:"status"`
	CurrentValue    string          `json:"current_value,omitempty"`
	EvidenceSummary string          `json:"evidence_summary"`
	ReviewReason    string          `json:"review_reason,omitempty"`
	Error           *task.Failure   `json:"error,omitempty"`
}

func decodeReport(stdout string) (report, error) {
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.DisallowUnknownFields()
	var value report
	if err := decoder.Decode(&value); err != nil {
		return report{}, fmt.Errorf("decode script check report: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		// Exactly one JSON value is allowed on stdout. Logs belong on stderr.
		if err == nil {
			return report{}, errors.New("decode script check report: trailing JSON value")
		}
		return report{}, fmt.Errorf("decode script check report: trailing content: %w", err)
	}
	if value.SchemaVersion != SchemaVersion {
		return report{}, fmt.Errorf("unsupported script check schema_version %q", value.SchemaVersion)
	}
	if len(value.Items) == 0 {
		return report{}, errors.New("script check report must contain at least one item")
	}
	return value, nil
}

func (definition *Definition) items(value report) ([]task.CheckItem, error) {
	metadata := definition.Metadata()
	definitions := make(map[string]plugin.CheckItemDefinition, len(metadata.Checks))
	for _, current := range metadata.Checks {
		definitions[current.ID] = current
	}
	seen := make(map[string]struct{}, len(value.Items))
	now := time.Now().UTC()
	items := make([]task.CheckItem, 0, len(value.Items))
	for index, observed := range value.Items {
		if _, duplicate := seen[observed.ID]; duplicate {
			return nil, fmt.Errorf("script check report contains duplicate item ID %q", observed.ID)
		}
		frozen, exists := definitions[observed.ID]
		if !exists {
			return nil, fmt.Errorf("script check report contains unknown item ID %q", observed.ID)
		}
		seen[observed.ID] = struct{}{}
		if strings.TrimSpace(observed.EvidenceSummary) == "" {
			return nil, fmt.Errorf("script check report item %q has empty evidence_summary", observed.ID)
		}
		item := task.CheckItem{
			ID: observed.ID, Status: observed.Status, Name: frozen.Name,
			CurrentValue: strings.TrimSpace(observed.CurrentValue),
			RecommendedValue: frozen.RecommendedValue,
			Risk: string(metadata.Risk), RiskDescription: frozen.Description,
			Remediation: "Use the Server remediation classification for this check result.",
			EvidenceSummary: strings.TrimSpace(observed.EvidenceSummary),
			ExecutedAt: now,
			ReviewReason: strings.TrimSpace(observed.ReviewReason),
			Error: cloneFailure(observed.Error),
		}
		switch observed.Status {
		case task.ItemSafe:
			if item.CurrentValue == "" {
				return nil, fmt.Errorf("script check report item %q safe status requires current_value", observed.ID)
			}
			value := true
			item.Applicable, item.Compliant = true, &value
		case task.ItemUnsafe:
			if item.CurrentValue == "" {
				return nil, fmt.Errorf("script check report item %q unsafe status requires current_value", observed.ID)
			}
			value := false
			item.Applicable, item.Compliant = true, &value
		case task.ItemManualReview:
			if item.CurrentValue == "" {
				return nil, fmt.Errorf("script check report item %q manual_review requires current_value", observed.ID)
			}
			item.Applicable = true
		case task.ItemError:
			if item.CurrentValue == "" {
				item.CurrentValue = "unavailable"
			}
			item.Applicable = true
		case task.ItemNotApplicable:
			if item.CurrentValue == "" {
				item.CurrentValue = "not applicable"
			}
			item.Applicable = false
		default:
			return nil, fmt.Errorf("script check report item %q has unsupported status %q", observed.ID, observed.Status)
		}
		if err := task.ValidateItem(item); err != nil {
			return nil, fmt.Errorf("script check report item[%d] %q: %w", index, observed.ID, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func canonicalParameters(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}", nil
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(&object); err != nil || object == nil {
		if err == nil {
			err = errors.New("parameters must be a JSON object")
		}
		return "", fmt.Errorf("decode script check parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("decode script check parameters: trailing JSON value")
		}
		return "", fmt.Errorf("decode script check parameters: trailing content: %w", err)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("encode script check parameters: %w", err)
	}
	return string(encoded), nil
}

func sensitiveParameterName(name string) bool {
	normalized := strings.ToLower(name)
	normalized = strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(normalized)
	for _, marker := range sensitiveParameterMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func cloneFailure(value *task.Failure) *task.Failure {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
