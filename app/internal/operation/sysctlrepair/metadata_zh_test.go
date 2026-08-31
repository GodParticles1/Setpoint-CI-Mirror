package sysctlrepair

import (
	"strings"
	"testing"
	"unicode"

	"setpoint/internal/operation"
)

func TestSysctlOperationMetadataIsChineseAndKeepsStableTechnicalKeys(t *testing.T) {
	metadata := Metadata()
	if metadata.ID != ID || metadata.Name != "ICMP Redirect 运行时修复" || metadata.Category != "Linux 运行时修复" {
		t.Fatalf("metadata=%#v", metadata)
	}
	for _, value := range []string{metadata.Description, metadata.Impact, metadata.Parameters[0].Description, metadata.Parameters[1].Description} {
		if !containsHan(value) {
			t.Fatalf("user metadata is not Chinese: %q", value)
		}
	}
	if metadata.Parameters[0].Name != "check_id" || metadata.Parameters[1].Name != "target_value" {
		t.Fatalf("technical parameter keys changed: %#v", metadata.Parameters)
	}
	if _, err := operation.CapabilityDigest(metadata); err != nil {
		t.Fatal(err)
	}
}

func containsHan(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}
