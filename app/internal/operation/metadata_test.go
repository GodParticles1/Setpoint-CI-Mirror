package operation

import "testing"

func TestMetadataCloneIsDeep(t *testing.T) {
	original := Metadata{ID: "operation.clickhouse.online_migration", Category: "data", Name: "ClickHouse migration", Version: "0.1.0", Risk: RiskHigh, SupportedSystems: []string{"linux"}, Parameters: []Parameter{{Name: "endpoint", Type: "object", Fields: []ParameterField{{Name: "host", Type: "string", Options: []string{"localhost"}}}}}}
	descriptor := NewMetadataDescriptor(original)
	original.SupportedSystems[0] = "changed"
	original.Parameters[0].Fields[0].Options[0] = "changed"
	got := descriptor.Metadata()
	if got.SupportedSystems[0] != "linux" || got.Parameters[0].Fields[0].Options[0] != "localhost" {
		t.Fatalf("metadata snapshot changed: %#v", got)
	}
}

func TestValidateMetadataRequiresBoundedObjectFields(t *testing.T) {
	base := Metadata{ID: "operation.test", Category: "test", Name: "Test", Version: "1", Risk: RiskLow, SupportedSystems: []string{"linux"}}
	tests := []struct {
		name      string
		parameter Parameter
	}{
		{name: "empty object", parameter: Parameter{Name: "endpoint", Type: "object"}},
		{name: "nested object", parameter: Parameter{Name: "endpoint", Type: "object", Fields: []ParameterField{{Name: "nested", Type: "object"}}}},
		{name: "duplicate field", parameter: Parameter{Name: "endpoint", Type: "object", Fields: []ParameterField{{Name: "host", Type: "string"}, {Name: "host", Type: "string"}}}},
		{name: "fields on scalar", parameter: Parameter{Name: "name", Type: "string", Fields: []ParameterField{{Name: "value", Type: "string"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := base
			metadata.Parameters = []Parameter{test.parameter}
			if err := ValidateMetadata(metadata); err == nil {
				t.Fatal("invalid parameter metadata accepted")
			}
		})
	}
}

func TestValidateTarget(t *testing.T) {
	if err := ValidateTarget(Target{Kind: TargetDataObject, Component: "clickhouse", Resource: "message_center.alarm"}); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
	if err := ValidateTarget(Target{Kind: TargetDataObject, Component: "clickhouse"}); err == nil {
		t.Fatal("missing data object resource accepted")
	}
}
