package app

import (
	"testing"
	"time"

	"setpoint/internal/deploymenttopology"
	"setpoint/internal/task"
)

func TestValidateDeploymentTopologyResultRejectsDerivedClassificationMismatch(t *testing.T) {
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	evidence := []deploymenttopology.Evidence{
		{ID: "a", Source: "fixture/a", Value: "dual relationship", Confidence: deploymenttopology.ConfidenceHigh, State: deploymenttopology.EvidenceObserved, TopologyKind: deploymenttopology.KindDual, Participants: []deploymenttopology.ParticipantFact{{Key: "local", Local: true}, {Key: "peer"}}},
		{ID: "b", Source: "fixture/b", Value: "standalone relationship", Confidence: deploymenttopology.ConfidenceHigh, State: deploymenttopology.EvidenceObserved, TopologyKind: deploymenttopology.KindStandalone, Participants: []deploymenttopology.ParticipantFact{{Key: "local", Local: true}}},
	}
	resolved, err := deploymenttopology.Resolve(evidence)
	if err != nil {
		t.Fatal(err)
	}
	resolved = deploymenttopology.WithParticipantTrust(resolved)
	spoofed := resolved
	spoofed.TopologyKind = deploymenttopology.KindDual
	spoofed.Status = deploymenttopology.StatusConfirmed
	resource := task.Resource{Kind: task.KindDeploymentTopologyDiscoveryTask, Status: task.Status{Phase: task.PhaseRunning}}
	submission := task.ResultSubmission{Phase: task.PhaseSucceeded, DeploymentTopologyResult: &task.DeploymentTopologyResult{StartedAt: now, CompletedAt: now.Add(time.Second), Topology: spoofed}}
	if err := validateDeploymentTopologyTaskResult(resource, &submission); err == nil {
		t.Fatal("expected server to reject derived classification that does not match evidence")
	}
}
