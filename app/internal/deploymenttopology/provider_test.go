package deploymenttopology

import (
	"context"
	"testing"
)

type fixtureProvider struct {
	id       string
	evidence []Evidence
}

func (provider fixtureProvider) ID() string { return provider.id }
func (provider fixtureProvider) Collect(context.Context) ([]Evidence, error) {
	return append([]Evidence(nil), provider.evidence...), nil
}

func TestProviderRegistryConflictingEvidenceIsAmbiguous(t *testing.T) {
	registry, err := NewProviderRegistry(
		fixtureProvider{id: "standalone", evidence: []Evidence{{ID: "a", Source: "fixture/standalone", Value: "standalone marker", Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindStandalone, Participants: []ParticipantFact{{Key: "local", Local: true}}}}},
		fixtureProvider{id: "dual", evidence: []Evidence{{ID: "b", Source: "fixture/dual", Value: "dual relationship marker", Confidence: ConfidenceHigh, State: EvidenceObserved, TopologyKind: KindDual, Participants: []ParticipantFact{{Key: "local", Local: true}, {Key: "peer", Addresses: []string{"10.0.0.2"}}}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TopologyKind != KindUnknown || result.Status != StatusAmbiguous {
		t.Fatalf("got %s/%s", result.TopologyKind, result.Status)
	}
	var peer *Participant
	for index := range result.Participants {
		if result.Participants[index].Key == "peer" {
			peer = &result.Participants[index]
		}
	}
	if peer == nil || peer.Trust != ParticipantTrustUntrusted {
		t.Fatalf("discovered peer trust = %#v", peer)
	}
}

func TestProviderRegistryEmptyIsUnsupported(t *testing.T) {
	registry, err := NewProviderRegistry()
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TopologyKind != KindUnknown || result.Status != StatusUnsupported {
		t.Fatalf("got %s/%s", result.TopologyKind, result.Status)
	}
}

func TestProviderRegistryRejectsDuplicateProviderID(t *testing.T) {
	_, err := NewProviderRegistry(fixtureProvider{id: "same"}, fixtureProvider{id: "same"})
	if err == nil {
		t.Fatal("expected duplicate provider id error")
	}
}
