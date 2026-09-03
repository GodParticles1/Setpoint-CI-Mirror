package plugins

import (
	"testing"

	"setpoint/internal/plugin"
)

func TestFormalCatalogHasExplicitRemediationDispositionForEveryCheck(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	definitions := registry.ListDefinitions()
	if len(definitions) != 84 {
		t.Fatalf("formal check count=%d, want 84", len(definitions))
	}
	counts := map[plugin.RemediationDisposition]int{}
	for _, definition := range definitions {
		if err := plugin.ValidateRemediationMetadata(definition.Remediation); err != nil {
			t.Fatalf("check %s has invalid remediation metadata: %v", definition.ID, err)
		}
		counts[definition.Remediation.Disposition]++
	}
	want := map[plugin.RemediationDisposition]int{
		plugin.RemediationAutoSafe:      4,
		plugin.RemediationControlled:    48,
		plugin.RemediationManualOnly:    27,
		plugin.RemediationNotApplicable: 5,
	}
	for disposition, expected := range want {
		if counts[disposition] != expected {
			t.Fatalf("disposition %s count=%d, want %d; all=%#v", disposition, counts[disposition], expected, counts)
		}
	}
	if len(counts) != len(want) {
		t.Fatalf("unexpected remediation disposition observed: %#v", counts)
	}
}

func TestFormalAutoSafeChecksBindOnlyApprovedICMPRedirectOperation(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{
		"net.ipv4.conf.all.accept_redirects.persisted":     {},
		"net.ipv4.conf.default.accept_redirects.persisted": {},
		"net.ipv4.conf.all.send_redirects.persisted":       {},
		"net.ipv4.conf.default.send_redirects.persisted":   {},
	}
	for _, definition := range registry.ListDefinitions() {
		if definition.Remediation.Disposition != plugin.RemediationAutoSafe {
			if definition.Remediation.OperationID != "" {
				t.Fatalf("non-AUTO_SAFE check %s unexpectedly binds operation %q", definition.ID, definition.Remediation.OperationID)
			}
			continue
		}
		if _, ok := want[definition.ID]; !ok {
			t.Fatalf("unexpected AUTO_SAFE check %s", definition.ID)
		}
		if definition.Remediation.OperationID != icmpRedirectRuntimeRepairOperationID {
			t.Fatalf("AUTO_SAFE check %s operation=%q", definition.ID, definition.Remediation.OperationID)
		}
		delete(want, definition.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing AUTO_SAFE checks: %#v", want)
	}
}
