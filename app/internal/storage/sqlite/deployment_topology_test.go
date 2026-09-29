package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/deploymenttopology"
	"setpoint/internal/task"
)

func TestDeploymentTopologyResultSurvivesSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "topology.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(
		ctx,
		`INSERT INTO nodes(id,hostname,os,os_version,arch,agent_version,registered_at,last_seen_at,updated_at,retired_at) VALUES(?,?,?,?,?,?,?,?,?,NULL)`,
		"node-a", "node-a", "linux", "test", "amd64", "test", formatTime(now), formatTime(now), formatTime(now),
	); err != nil {
		t.Fatal(err)
	}
	resource := task.Resource{
		APIVersion: "setpoint.io/v1",
		Kind:       task.KindDeploymentTopologyDiscoveryTask,
		Metadata: task.Metadata{
			ID:             "topology-task-1",
			IdempotencyKey: "topology-idem-1",
			CreatedAt:      now,
		},
		Spec: task.Spec{
			NodeID:     "node-a",
			Parameters: json.RawMessage(`{}`),
		},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: now},
	}
	if _, _, err := store.CreateTask(ctx, resource); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTask(ctx, "node-a", "claim-1", now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %#v", err, claimed)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-a", resource.Metadata.ID, "claim-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	evidence := []deploymenttopology.Evidence{
		{
			ID:           "dual-a",
			Source:       "fixture/dual",
			Value:        "explicit dual relationship",
			Confidence:   deploymenttopology.ConfidenceHigh,
			State:        deploymenttopology.EvidenceObserved,
			TopologyKind: deploymenttopology.KindDual,
			Participants: []deploymenttopology.ParticipantFact{
				{Key: "node-a", Local: true},
				{Key: "node-b", Addresses: []string{"10.0.0.2"}},
			},
		},
		{
			ID:           "single-b",
			Source:       "fixture/single",
			Value:        "conflicting standalone marker",
			Confidence:   deploymenttopology.ConfidenceHigh,
			State:        deploymenttopology.EvidenceObserved,
			TopologyKind: deploymenttopology.KindStandalone,
			Participants: []deploymenttopology.ParticipantFact{{Key: "node-a", Local: true}},
		},
	}
	resolved, err := deploymenttopology.Resolve(evidence)
	if err != nil {
		t.Fatal(err)
	}
	resolved = deploymenttopology.WithParticipantTrust(resolved)
	if resolved.TopologyKind != deploymenttopology.KindUnknown || resolved.Status != deploymenttopology.StatusAmbiguous {
		t.Fatalf("fixture must be ambiguous, got %s/%s", resolved.TopologyKind, resolved.Status)
	}
	result := task.DeploymentTopologyResult{
		StartedAt:   now.Add(2 * time.Second),
		CompletedAt: now.Add(3 * time.Second),
		Topology:    resolved,
	}
	if _, err := store.CompleteTask(ctx, "node-a", resource.Metadata.ID, task.ResultSubmission{
		ClaimID:                  "claim-1",
		Phase:                    task.PhaseSucceeded,
		DeploymentTopologyResult: &result,
	}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.GetTask(ctx, resource.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Kind != task.KindDeploymentTopologyDiscoveryTask || loaded.DeploymentTopologyResult == nil {
		t.Fatalf("typed topology result not reconstructed: %#v", loaded)
	}
	if loaded.DeploymentTopologyResult.Topology.TopologyKind != deploymenttopology.KindUnknown ||
		loaded.DeploymentTopologyResult.Topology.Status != deploymenttopology.StatusAmbiguous {
		t.Fatalf(
			"reopened topology = %s/%s",
			loaded.DeploymentTopologyResult.Topology.TopologyKind,
			loaded.DeploymentTopologyResult.Topology.Status,
		)
	}
	if len(loaded.DeploymentTopologyResult.Topology.Conflicts) == 0 {
		t.Fatal("conflict evidence was not durable")
	}
	for _, participant := range loaded.DeploymentTopologyResult.Topology.Participants {
		if !participant.Local && participant.Trust != deploymenttopology.ParticipantTrustUntrusted {
			t.Fatalf("peer trust after reopen = %q", participant.Trust)
		}
	}
}
