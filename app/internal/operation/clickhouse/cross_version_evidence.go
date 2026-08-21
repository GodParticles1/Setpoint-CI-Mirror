package clickhouse

import "strings"

type CrossVersionEvidenceStage string

const (
	CrossVersionEvidenceNone           CrossVersionEvidenceStage = "none"
	CrossVersionEvidenceRoundTrip      CrossVersionEvidenceStage = "round_trip_verified"
	CrossVersionEvidenceFullyQualified CrossVersionEvidenceStage = "fully_qualified"
)

type CrossVersionLabEvidence struct {
	EvidenceID              string            `json:"evidence_id"`
	SourceVersion           string            `json:"source_version"`
	TargetVersion           string            `json:"target_version"`
	Strategy                StrategyID        `json:"strategy"`
	VerifiedTypes           []string          `json:"verified_types,omitempty"`
	Rows                     uint64            `json:"rows"`
	ReplicaCount             int               `json:"replica_count"`
	SourceFingerprint        string            `json:"source_fingerprint,omitempty"`
	ReplicaFingerprints      []string          `json:"replica_fingerprints,omitempty"`
	RoundTripVerified        bool              `json:"round_trip_verified"`
	ReplicaConvergence       bool              `json:"replica_convergence"`
	ReconcileWithoutRewrite  bool              `json:"reconcile_without_rewrite"`
	UnsupportedTypeFailClose bool              `json:"unsupported_type_fail_close"`
	RollbackRunOwnedOnly     bool              `json:"rollback_run_owned_only"`
	CleanupVerified          bool              `json:"cleanup_verified"`
	Stage                    CrossVersionEvidenceStage `json:"stage"`
}

type CrossVersionEvidenceDecision struct {
	Stage               CrossVersionEvidenceStage `json:"stage"`
	RegistryRuleEligible bool                     `json:"registry_rule_eligible"`
	Reason              string                   `json:"reason"`
}

func EvaluateCrossVersionLabEvidence(evidence CrossVersionLabEvidence) CrossVersionEvidenceDecision {
	if strings.TrimSpace(evidence.EvidenceID) == "" || strings.TrimSpace(evidence.SourceVersion) == "" || strings.TrimSpace(evidence.TargetVersion) == "" {
		return CrossVersionEvidenceDecision{Stage: CrossVersionEvidenceNone, Reason: "evidence identity and exact source/target versions are required"}
	}
	if evidence.Strategy == "" || evidence.Rows == 0 || len(evidence.VerifiedTypes) == 0 {
		return CrossVersionEvidenceDecision{Stage: CrossVersionEvidenceNone, Reason: "strategy, row count and verified type envelope are required"}
	}
	if !evidence.RoundTripVerified || !evidence.ReplicaConvergence || strings.TrimSpace(evidence.SourceFingerprint) == "" {
		return CrossVersionEvidenceDecision{Stage: CrossVersionEvidenceNone, Reason: "physical round-trip and convergence evidence are incomplete"}
	}
	if evidence.ReplicaCount < 1 || len(evidence.ReplicaFingerprints) != evidence.ReplicaCount {
		return CrossVersionEvidenceDecision{Stage: CrossVersionEvidenceNone, Reason: "every expected replica must contribute a fingerprint"}
	}
	for _, fingerprint := range evidence.ReplicaFingerprints {
		if strings.TrimSpace(fingerprint) == "" || fingerprint != evidence.SourceFingerprint {
			return CrossVersionEvidenceDecision{Stage: CrossVersionEvidenceNone, Reason: "replica fingerprints must exactly match the source fingerprint"}
		}
	}
	if !evidence.ReconcileWithoutRewrite || !evidence.UnsupportedTypeFailClose || !evidence.RollbackRunOwnedOnly || !evidence.CleanupVerified {
		return CrossVersionEvidenceDecision{
			Stage: CrossVersionEvidenceRoundTrip,
			Reason: "Native round-trip is physically verified for the declared type envelope, but restart/fail-close/rollback/cleanup qualification is incomplete",
		}
	}
	return CrossVersionEvidenceDecision{
		Stage: CrossVersionEvidenceFullyQualified,
		RegistryRuleEligible: true,
		Reason: "the directional strategy has complete physical evidence for the declared schema/type envelope",
	}
}
