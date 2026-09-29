package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/deploymenttopology"
	"setpoint/internal/task"
)

type topologyFixtureProvider struct {
	id       string
	evidence []deploymenttopology.Evidence
}

func (provider topologyFixtureProvider) ID() string { return provider.id }
func (provider topologyFixtureProvider) Collect(context.Context) ([]deploymenttopology.Evidence, error) {
	return append([]deploymenttopology.Evidence(nil), provider.evidence...), nil
}

type topologyWorkerRemote struct {
	resource   task.Resource
	claimed    bool
	submission *task.ResultSubmission
}

func (remote *topologyWorkerRemote) ClaimTask(context.Context, string) (*task.Resource, error) {
	if remote.claimed {
		return nil, nil
	}
	remote.claimed = true
	copy := task.Clone(remote.resource)
	return &copy, nil
}

func (remote *topologyWorkerRemote) AcknowledgeTask(context.Context, string, string, string) (task.Resource, error) {
	copy := task.Clone(remote.resource)
	copy.Status.Phase = task.PhaseRunning
	return copy, nil
}

func (remote *topologyWorkerRemote) SubmitTaskResult(_ context.Context, _, _ string, submission task.ResultSubmission) (task.Resource, error) {
	copy := submission
	remote.submission = &copy
	result := task.Clone(remote.resource)
	result.Status.Phase = submission.Phase
	result.DeploymentTopologyResult = submission.DeploymentTopologyResult
	return result, nil
}

func TestTaskWorkerExecutesTopologyRegistryAndPreservesAmbiguity(t *testing.T) {
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	resource := task.Resource{
		APIVersion: "setpoint.io/v1",
		Kind:       task.KindDeploymentTopologyDiscoveryTask,
		Metadata: task.Metadata{
			ID:             "topology-task",
			IdempotencyKey: "topology-idem",
			CreatedAt:      now,
		},
		Spec: task.Spec{
			NodeID:     "agent-1",
			Parameters: json.RawMessage(`{}`),
		},
		Status: task.Status{
			Phase:     task.PhaseClaimed,
			ClaimID:   "claim-1",
			Attempt:   1,
			UpdatedAt: now,
		},
	}
	remote := &topologyWorkerRemote{resource: resource}
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "topology-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewTaskWorker(remote, "agent-1", "linux", testWorkerRegistry(t), &workerExecutor{}, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deploymenttopology.NewProviderRegistry(
		topologyFixtureProvider{
			id: "dual",
			evidence: []deploymenttopology.Evidence{{
				ID:           "a",
				Source:       "fixture/dual",
				Value:        "explicit dual relationship",
				Confidence:   deploymenttopology.ConfidenceHigh,
				State:        deploymenttopology.EvidenceObserved,
				TopologyKind: deploymenttopology.KindDual,
				Participants: []deploymenttopology.ParticipantFact{
					{Key: "agent-1", Local: true},
					{Key: "peer-1", Addresses: []string{"10.0.0.2"}},
				},
			}},
		},
		topologyFixtureProvider{
			id: "standalone",
			evidence: []deploymenttopology.Evidence{{
				ID:           "b",
				Source:       "fixture/standalone",
				Value:        "conflicting standalone relationship",
				Confidence:   deploymenttopology.ConfidenceHigh,
				State:        deploymenttopology.EvidenceObserved,
				TopologyKind: deploymenttopology.KindStandalone,
				Participants: []deploymenttopology.ParticipantFact{{Key: "agent-1", Local: true}},
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.SetTopologyProviderRegistry(registry); err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if remote.submission == nil || remote.submission.Phase != task.PhaseSucceeded || remote.submission.DeploymentTopologyResult == nil {
		t.Fatalf("submission=%#v", remote.submission)
	}
	resolved := remote.submission.DeploymentTopologyResult.Topology
	if resolved.TopologyKind != deploymenttopology.KindUnknown || resolved.Status != deploymenttopology.StatusAmbiguous {
		t.Fatalf("resolved=%s/%s", resolved.TopologyKind, resolved.Status)
	}
	foundPeer := false
	for _, participant := range resolved.Participants {
		if participant.Key == "peer-1" {
			foundPeer = true
			if participant.Trust != deploymenttopology.ParticipantTrustUntrusted {
				t.Fatalf("peer trust=%q", participant.Trust)
			}
		}
	}
	if !foundPeer {
		t.Fatal("peer missing")
	}
}
