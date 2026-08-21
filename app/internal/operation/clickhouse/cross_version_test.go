package clickhouse

import "testing"

func TestCrossVersionPolicyAllowsSameReleaseLineForAnalysis(t *testing.T) {
	decision := NewCrossVersionRegistry().Decide("23.12.1.9", "23.12.4.1")
	if decision.State != CrossVersionAllowed || decision.Direction != CrossVersionSame || decision.Relation != VersionRelationPatchDifferent {
		t.Fatalf("decision=%#v", decision)
	}
	if len(decision.Strategies) != 1 || decision.Strategies[0] != StrategyNativeStream {
		t.Fatalf("strategies=%v", decision.Strategies)
	}
}

func TestCrossVersionPolicyBlocksUnknownDowngradeUntilEvidenceExists(t *testing.T) {
	decision := NewCrossVersionRegistry().Decide("23.12.1.9", "20.3.5.21")
	if decision.State != CrossVersionUnknown || decision.Direction != CrossVersionDowngrade || decision.Relation != VersionRelationMajorDifferent {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestCrossVersionPolicyCanEnableOnlyExactPhysicallyProvenPair(t *testing.T) {
	registry := NewCrossVersionRegistry(CrossVersionRule{
		SourceVersion: "23.12.1.9", TargetVersion: "20.3.5.21",
		State: CrossVersionAllowed,
		Strategies: []StrategyID{StrategyNativeStream, StrategyNativeStream},
		Evidence: "physical_lab:fixture-23.12.1.9-to-20.3.5.21",
		Reason: "isolated physical compatibility suite passed for the declared exact version pair and schema/type envelope",
	})
	decision := registry.Decide("23.12.1.9", "20.3.5.21")
	if decision.State != CrossVersionAllowed || decision.Direction != CrossVersionDowngrade {
		t.Fatalf("decision=%#v", decision)
	}
	if len(decision.Strategies) != 1 || decision.Strategies[0] != StrategyNativeStream {
		t.Fatalf("strategies=%v", decision.Strategies)
	}
	if decision.Evidence == "" {
		t.Fatal("physical evidence reference is required")
	}
}

func TestCrossVersionPolicyDoesNotGeneralizeExactEvidenceToNearbyPatch(t *testing.T) {
	registry := NewCrossVersionRegistry(CrossVersionRule{
		SourceVersion: "23.12.1.9", TargetVersion: "20.3.5.21",
		State: CrossVersionAllowed, Strategies: []StrategyID{StrategyNativeStream}, Evidence: "lab",
	})
	for _, pair := range [][2]string{
		{"23.12.2.1", "20.3.5.21"},
		{"23.12.1.9", "20.3.6.1"},
	} {
		decision := registry.Decide(pair[0], pair[1])
		if decision.State != CrossVersionUnknown {
			t.Fatalf("pair=%v decision=%#v", pair, decision)
		}
	}
}

func TestCrossVersionPolicyDoesNotGeneralizeOneDirectionToTheOther(t *testing.T) {
	registry := NewCrossVersionRegistry(CrossVersionRule{SourceVersion: "23.12.1.9", TargetVersion: "20.3.5.21", State: CrossVersionAllowed, Strategies: []StrategyID{StrategyNativeStream}, Evidence: "lab"})
	upgrade := registry.Decide("20.3.5.21", "23.12.1.9")
	if upgrade.State != CrossVersionUnknown || upgrade.Direction != CrossVersionUpgrade {
		t.Fatalf("upgrade=%#v", upgrade)
	}
}
