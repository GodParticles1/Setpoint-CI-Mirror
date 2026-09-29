package deploymenttopology

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestResolveFixtureMatrix(t *testing.T) {
	tests := []struct {
		name          string
		evidence      []Evidence
		wantKind      Kind
		wantStatus    Status
		wantFinding   string
		wantConflict  string
		wantLocalRole string
		wantMembers   int
	}{
		{
			name: "standalone",
			evidence: []Evidence{{
				ID: "product-layout", Source: "generic.product-layout", Value: "one local runtime instance and explicit standalone mode",
				Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindStandalone, LocalRole: "member",
				Participants: []ParticipantFact{{Key: "node-a", Hostname: "node-a", Addresses: []string{"10.0.0.10"}, Role: "member", Local: true}},
			}},
			wantKind: KindStandalone, wantStatus: StatusConfirmed, wantLocalRole: "member", wantMembers: 1,
		},
		{
			name: "dual",
			evidence: []Evidence{
				{
					ID: "dual-config", Source: "generic.dual-config", Value: "master=10.0.0.10 slave=10.0.0.11",
					Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual, LocalRole: "master",
					Participants: []ParticipantFact{
						{Key: "node-a", Hostname: "node-a", Addresses: []string{"10.0.0.10"}, Role: "master", Local: true},
						{Key: "node-b", Hostname: "node-b", Addresses: []string{"10.0.0.11"}, Role: "standby"},
					},
				},
				{
					ID: "keepalived", Source: "generic.keepalived", Value: "unicast peer and VIP configuration agree with dual config",
					Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual, LocalRole: "master",
					Participants: []ParticipantFact{
						{Key: "node-a", Addresses: []string{"10.0.0.10"}, Role: "master", Local: true},
						{Key: "node-b", Addresses: []string{"10.0.0.11"}, Role: "standby"},
					},
				},
			},
			wantKind: KindDual, wantStatus: StatusConfirmed, wantLocalRole: "master", wantMembers: 2,
		},
		{
			name: "cluster",
			evidence: []Evidence{{
				ID: "nodes-yaml", Source: "generic.nodes-yaml", Value: "three explicit service participants",
				Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindCluster, LocalRole: "member",
				Participants: []ParticipantFact{
					{Key: "node-a", Hostname: "node-a", Local: true},
					{Key: "node-b", Hostname: "node-b"},
					{Key: "node-c", Hostname: "node-c"},
				},
			}},
			wantKind: KindCluster, wantStatus: StatusConfirmed, wantLocalRole: "member", wantMembers: 3,
		},
		{
			name: "conflicting",
			evidence: []Evidence{
				{
					ID: "nodes-yaml", Source: "generic.nodes-yaml", Value: "two explicit members",
					Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual,
					Participants: []ParticipantFact{{Key: "node-a", Local: true}, {Key: "node-b"}},
				},
				{
					ID: "runtime-layout", Source: "generic.product-layout", Value: "runtime declares standalone mode",
					Confidence: ConfidenceMedium, State: EvidenceObserved, TopologyKind: KindStandalone,
					Participants: []ParticipantFact{{Key: "node-a", Local: true}},
				},
			},
			wantKind: KindUnknown, wantStatus: StatusAmbiguous, wantConflict: "topology_kind", wantMembers: 2,
		},
		{
			name: "incomplete",
			evidence: []Evidence{{
				ID: "dual-config", Source: "generic.dual-config", Value: "dual mode is explicit but peer identity is missing",
				Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual, LocalRole: "master",
				Participants: []ParticipantFact{{Key: "node-a", Local: true, Role: "master"}},
			}},
			wantKind: KindDual, wantStatus: StatusAmbiguous, wantFinding: "participants_incomplete", wantLocalRole: "master", wantMembers: 1,
		},
		{
			name: "participant count alone does not infer dual",
			evidence: []Evidence{{
				ID: "processes", Source: "generic.process-layout", Value: "two addresses were observed without a relationship contract",
				Confidence: ConfidenceLow, State: EvidenceObserved, TopologyKind: KindUnknown,
				Participants: []ParticipantFact{{Key: "node-a", Local: true}, {Key: "node-b"}},
			}},
			wantKind: KindUnknown, wantStatus: StatusAmbiguous, wantFinding: "topology_kind_unresolved", wantMembers: 2,
		},
		{
			name: "unsupported",
			evidence: []Evidence{{
				ID: "provider", Source: "generic.product-detector", Value: "no supported product topology provider matched",
				Confidence: ConfidenceHigh, State: EvidenceUnsupported, TopologyKind: KindUnknown,
			}},
			wantKind: KindUnknown, wantStatus: StatusUnsupported,
		},
		{
			name: "participant role conflict",
			evidence: []Evidence{
				{
					ID: "dual-config", Source: "generic.dual-config", Value: "local role is master",
					Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual,
					Participants: []ParticipantFact{{Key: "node-a", Role: "master", Local: true}, {Key: "node-b", Role: "standby"}},
				},
				{
					ID: "runtime-role", Source: "generic.runtime-role", Value: "same participant reports standby",
					Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual,
					Participants: []ParticipantFact{{Key: "node-a", Role: "standby", Local: true}, {Key: "node-b", Role: "master"}},
				},
			},
			wantKind: KindDual, wantStatus: StatusAmbiguous, wantConflict: "participants.role", wantMembers: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Resolve(test.evidence)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if result.SchemaVersion != SchemaVersion {
				t.Fatalf("SchemaVersion = %q, want %q", result.SchemaVersion, SchemaVersion)
			}
			if result.TopologyKind != test.wantKind || result.Status != test.wantStatus {
				t.Fatalf("kind/status = %q/%q, want %q/%q; result=%+v", result.TopologyKind, result.Status, test.wantKind, test.wantStatus, result)
			}
			if result.LocalRole != test.wantLocalRole {
				t.Fatalf("LocalRole = %q, want %q", result.LocalRole, test.wantLocalRole)
			}
			if len(result.Participants) != test.wantMembers {
				t.Fatalf("participants = %d, want %d", len(result.Participants), test.wantMembers)
			}
			if test.wantFinding != "" && !hasFinding(result, test.wantFinding) {
				t.Fatalf("missing finding %q in %+v", test.wantFinding, result.Findings)
			}
			if test.wantConflict != "" && !hasConflict(result, test.wantConflict) {
				t.Fatalf("missing conflict %q in %+v", test.wantConflict, result.Conflicts)
			}
		})
	}
}

func TestResolveRejectsMalformedEvidence(t *testing.T) {
	_, err := Resolve([]Evidence{
		{ID: "same", Source: "one", Value: "value", Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindUnknown},
		{ID: "same", Source: "two", Value: "value", Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindUnknown},
	})
	if !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("error = %v, want ErrInvalidEvidence", err)
	}
}

func TestResolveEmptyInputIsUnsupported(t *testing.T) {
	result, err := Resolve(nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if result.TopologyKind != KindUnknown || result.Status != StatusUnsupported {
		t.Fatalf("result = %+v, want unknown/unsupported", result)
	}
	if result.Participants == nil || result.Evidence == nil {
		t.Fatalf("result collections must be empty arrays, got %+v", result)
	}
}

func TestResultJSONContract(t *testing.T) {
	result, err := Resolve([]Evidence{{
		ID: "single", Source: "generic.product-layout", Value: "explicit standalone",
		Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindStandalone,
		Participants: []ParticipantFact{{Key: "node-a", Local: true}},
	}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, field := range []string{"schema_version", "topology_kind", "participants", "evidence", "status", "summary"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("JSON contract missing %q: %s", field, encoded)
		}
	}
}

func hasFinding(result Result, code string) bool {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func hasConflict(result Result, field string) bool {
	for _, conflict := range result.Conflicts {
		if conflict.Field == field {
			return true
		}
	}
	return false
}
