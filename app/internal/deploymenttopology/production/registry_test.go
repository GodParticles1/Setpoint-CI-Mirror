package production

import (
	"testing"

	"setpoint/internal/deploymenttopology/providers/xrocketnodes"
)

func TestProductionProvidersAreNonEmptyAndDeterministic(t *testing.T) {
	first := Providers()
	second := Providers()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("production providers = %d/%d, want 1/1", len(first), len(second))
	}
	if first[0].ID() != xrocketnodes.ProviderID || second[0].ID() != xrocketnodes.ProviderID {
		t.Fatalf("provider order = %q/%q", first[0].ID(), second[0].ID())
	}
	if _, err := NewRegistry(); err != nil {
		t.Fatal(err)
	}
}
