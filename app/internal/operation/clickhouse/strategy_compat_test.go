package clickhouse

import "testing"

func supportedProfile(version string, capabilities ...CapabilityID) CapabilityProfile {
	profile := NewCapabilityProfile(version)
	for _, capability := range capabilities {
		profile.Set(capability, CapabilitySupported, "runtime_probe", "test")
	}
	return profile
}

func TestSelectCompatiblePrefersBackupWhenBothEndpointsProveIt(t *testing.T) {
	selector := NewStrategySelector()
	intersection := CapabilityIntersection{
		Source: supportedProfile("24.8.1.1", CapabilityNativeFormat, CapabilityBuiltinBackupRestore),
		Target: supportedProfile("24.8.1.1", CapabilityNativeFormat, CapabilityBuiltinBackupRestore),
	}
	decision := selector.SelectCompatible(Parameters{}, Snapshot{}, Snapshot{}, PrecheckReport{Compatible: true}, intersection)
	if decision.Selected != StrategyBuiltinBackupRestore { t.Fatalf("selected=%q", decision.Selected) }
	if len(decision.Candidates) != 2 || decision.Candidates[1] != StrategyNativeStream { t.Fatalf("candidates=%v", decision.Candidates) }
}

func TestSelectCompatibleFallsBackToNativeForMixedCapabilities(t *testing.T) {
	selector := NewStrategySelector()
	intersection := CapabilityIntersection{
		Source: supportedProfile("24.8.1.1", CapabilityNativeFormat, CapabilityBuiltinBackupRestore),
		Target: supportedProfile("21.8.1.1", CapabilityNativeFormat),
	}
	decision := selector.SelectCompatible(Parameters{}, Snapshot{}, Snapshot{}, PrecheckReport{Compatible: true}, intersection)
	if decision.Selected != StrategyNativeStream { t.Fatalf("selected=%q", decision.Selected) }
}

func TestSelectCompatibleDoesNotInferFeatureFromVersionText(t *testing.T) {
	selector := NewStrategySelector()
	intersection := CapabilityIntersection{Source: NewCapabilityProfile("25.8.1.1"), Target: NewCapabilityProfile("25.8.1.1")}
	decision := selector.SelectCompatible(Parameters{}, Snapshot{}, Snapshot{}, PrecheckReport{Compatible: true}, intersection)
	if decision.Selected != "" { t.Fatalf("selected=%q", decision.Selected) }
}

func TestSelectCompatibleTimeRangeRequiresNativeCapability(t *testing.T) {
	selector := NewStrategySelector()
	intersection := CapabilityIntersection{
		Source: supportedProfile("24.8.1.1", CapabilityNativeFormat, CapabilityBuiltinBackupRestore),
		Target: supportedProfile("24.8.1.1", CapabilityNativeFormat, CapabilityBuiltinBackupRestore),
	}
	decision := selector.SelectCompatible(Parameters{StartTime: "2026-08-01T00:00:00Z", EndTime: "2026-08-02T00:00:00Z"}, Snapshot{}, Snapshot{}, PrecheckReport{Compatible: true}, intersection)
	if decision.Selected != StrategyNativeStream { t.Fatalf("selected=%q", decision.Selected) }
}
