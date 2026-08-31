package clickhouse

import (
	"strings"
	"testing"
	"unicode"

	"setpoint/internal/operation"
)

func TestClickHouseOperationMetadataIsChineseAndKeepsStableTechnicalKeys(t *testing.T) {
	metadata := OperationMetadata()
	if metadata.ID != OperationID || metadata.Name != "ClickHouse 在线迁移" || metadata.Category != "数据迁移" {
		t.Fatalf("metadata=%#v", metadata)
	}
	for _, value := range []string{metadata.Description, metadata.Impact} {
		if !containsChineseMetadata(value) {
			t.Fatalf("user metadata is not Chinese: %q", value)
		}
	}
	wantParameters := []string{"source", "target", "database", "tables", "time_column", "start_time", "end_time"}
	for index, parameter := range metadata.Parameters {
		if parameter.Name != wantParameters[index] || !containsChineseMetadata(parameter.Description) {
			t.Fatalf("parameter[%d]=%#v", index, parameter)
		}
		for _, field := range parameter.Fields {
			if !containsChineseMetadata(field.Description) {
				t.Fatalf("field=%#v", field)
			}
		}
	}
	for _, secret := range metadata.SecretRequirements {
		if !containsChineseMetadata(secret.Description) {
			t.Fatalf("secret requirement=%#v", secret)
		}
	}
	if _, err := operation.CapabilityDigest(metadata); err != nil {
		t.Fatal(err)
	}
}

func containsChineseMetadata(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}
