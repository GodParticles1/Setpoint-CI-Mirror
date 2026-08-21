package clickhouse

import "testing"

func TestObserved2312To2003EvidenceRemainsPartialWithoutPhysicalRollback(t *testing.T) {
	profile, ok, err := LoadCompatibilityEvidenceProfile("23.12.1.9", "20.3.5.21", StrategyNativeStream)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected exact compatibility evidence profile")
	}
	fingerprint := "87d59b4f0c9d818c342e7188e6663c6fbfa41faa33558bba25fca182d8f64c45"
	decision := EvaluateCrossVersionLabEvidence(CrossVersionLabEvidence{
		EvidenceID: "physical_lab:23.12.1.9-to-20.3.5.21:20260812",
		SourceVersion: "23.12.1.9", TargetVersion: "20.3.5.21",
		Strategy: StrategyNativeStream,
		VerifiedTypes: profile.VerifiedTypes,
		Rows: 128, ReplicaCount: 3,
		SourceFingerprint: fingerprint,
		ReplicaFingerprints: []string{fingerprint, fingerprint, fingerprint},
		RoundTripVerified: true,
		ReplicaConvergence: true,
		ReconcileWithoutRewrite: true,
		UnsupportedTypeFailClose: true,
		CleanupVerified: true,
		RollbackRunOwnedOnly: false,
	})
	if decision.Stage != CrossVersionEvidenceRoundTrip {
		t.Fatalf("stage=%s reason=%s", decision.Stage, decision.Reason)
	}
	if decision.RegistryRuleEligible {
		t.Fatal("physical reconcile/fail-close/cleanup evidence must not enable Apply before run-owned rollback is physically proven")
	}
}

func TestCrossVersionEvidenceBecomesEligibleOnlyWhenAllSafetyEvidenceExists(t *testing.T) {
	fingerprint := "fp"
	decision := EvaluateCrossVersionLabEvidence(CrossVersionLabEvidence{
		EvidenceID: "lab", SourceVersion: "23.12.1.9", TargetVersion: "20.3.5.21",
		Strategy: StrategyNativeStream, VerifiedTypes: []string{"UInt64"}, Rows: 1, ReplicaCount: 3,
		SourceFingerprint: fingerprint, ReplicaFingerprints: []string{fingerprint, fingerprint, fingerprint},
		RoundTripVerified: true, ReplicaConvergence: true, ReconcileWithoutRewrite: true,
		UnsupportedTypeFailClose: true, RollbackRunOwnedOnly: true, CleanupVerified: true,
	})
	if decision.Stage != CrossVersionEvidenceFullyQualified || !decision.RegistryRuleEligible {
		t.Fatalf("decision=%#v", decision)
	}
}
