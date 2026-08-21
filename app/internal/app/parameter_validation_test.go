package app

import (
	"encoding/json"
	"strings"
	"testing"

	"setpoint/internal/plugin"
)

func TestValidateParameterValuesStringMatrix(t *testing.T) {
	metadata := plugin.Metadata{Parameters: []plugin.Parameter{{Name: "value", Type: "string"}}}
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{name: "string", raw: `"value"`, ok: true},
		{name: "number", raw: `8`},
		{name: "boolean", raw: `true`},
		{name: "null", raw: `null`},
		{name: "array", raw: `[]`},
		{name: "object", raw: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateParameterNames(metadata, map[string]json.RawMessage{"value": json.RawMessage(test.raw)})
			if test.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("invalid value was accepted")
			}
		})
	}
}

func TestValidateParameterValuesOptionsUseExactMembership(t *testing.T) {
	metadata := plugin.Metadata{Parameters: []plugin.Parameter{{
		Name: "host_role", Type: "string", Options: []string{"unknown", "non_gateway", "gateway"},
	}}}
	for _, option := range metadata.Parameters[0].Options {
		t.Run(option, func(t *testing.T) {
			raw, err := json.Marshal(option)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateParameterNames(metadata, map[string]json.RawMessage{"host_role": raw}); err != nil {
				t.Fatalf("valid option rejected: %v", err)
			}
		})
	}
	for _, raw := range []string{`"whatever"`, `" gateway "`, `"GATEWAY"`, `123`} {
		err := validateParameterNames(metadata, map[string]json.RawMessage{"host_role": json.RawMessage(raw)})
		if err == nil {
			t.Fatalf("invalid option accepted: %s", raw)
		}
		if strings.Contains(err.Error(), strings.Trim(raw, `"`)) {
			t.Fatalf("error leaked raw parameter value: %v", err)
		}
	}
}

func TestValidateParameterValuesIntegerMatrix(t *testing.T) {
	metadata := plugin.Metadata{Parameters: []plugin.Parameter{{Name: "target", Type: "integer"}}}
	tests := []struct {
		raw string
		ok  bool
	}{
		{raw: `8`, ok: true},
		{raw: `-16`, ok: true},
		{raw: `"8"`},
		{raw: `8.5`},
		{raw: `true`},
		{raw: `null`},
		{raw: `[]`},
		{raw: `{}`},
		{raw: `9223372036854775808`},
	}
	for _, test := range tests {
		err := validateParameterNames(metadata, map[string]json.RawMessage{"target": json.RawMessage(test.raw)})
		if test.ok && err != nil {
			t.Fatalf("integer %s rejected: %v", test.raw, err)
		}
		if !test.ok && err == nil {
			t.Fatalf("invalid integer accepted: %s", test.raw)
		}
	}
}

func TestValidateParameterValuesPreservesNameRequiredAndOptionalRules(t *testing.T) {
	metadata := plugin.Metadata{Parameters: []plugin.Parameter{
		{Name: "required", Type: "string", Required: true},
		{Name: "optional", Type: "integer"},
	}}
	if err := validateParameterNames(metadata, map[string]json.RawMessage{"unknown": json.RawMessage(`"x"`)}); err == nil {
		t.Fatal("unknown parameter accepted")
	}
	if err := validateParameterNames(metadata, map[string]json.RawMessage{}); err == nil {
		t.Fatal("missing required parameter accepted")
	}
	if err := validateParameterNames(metadata, map[string]json.RawMessage{"required": json.RawMessage(`"x"`)}); err != nil {
		t.Fatalf("optional absence rejected: %v", err)
	}
}

func TestValidateParameterValuesDoesNotPromotePluginBusinessRules(t *testing.T) {
	nginx := plugin.Metadata{Parameters: []plugin.Parameter{{Name: "cors_allowed_origins", Type: "string"}}}
	if err := validateParameterNames(nginx, map[string]json.RawMessage{"cors_allowed_origins": json.RawMessage(`"not-an-origin"`)}); err != nil {
		t.Fatalf("generic validator promoted Nginx grammar: %v", err)
	}
	password := plugin.Metadata{Parameters: []plugin.Parameter{{Name: "pwquality_min_length_target", Type: "integer"}}}
	if err := validateParameterNames(password, map[string]json.RawMessage{"pwquality_min_length_target": json.RawMessage(`999`)}); err != nil {
		t.Fatalf("generic validator promoted password range: %v", err)
	}
}

func TestValidateParameterValuesFailsClosedOnUnsupportedType(t *testing.T) {
	metadata := plugin.Metadata{Parameters: []plugin.Parameter{{Name: "value", Type: "boolean"}}}
	if err := validateParameterNames(metadata, map[string]json.RawMessage{}); err == nil {
		t.Fatal("unsupported parameter type was accepted")
	}
}
