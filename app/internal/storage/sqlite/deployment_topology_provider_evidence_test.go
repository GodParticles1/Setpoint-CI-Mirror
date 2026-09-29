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

func TestDeploymentTopologyProviderEvidenceSurvivesSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "topology-provider.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
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
			ID:             "topology-provider-task-1",
			IdempotencyKey: "topology-provider-idem-1",
			CreatedAt:      now,
		},
		Spec:   task.Spec{NodeID: "node-a", Parameters: json.RawMessage(`{}`)},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: now},
	}
	if _, _, err := store.CreateTask(ctx, resource); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimTask(ctx, "node-a", "claim-provider-1", now.Add(time.Second))
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %#v", err, claimed)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-a", resource.Metadata.ID, "claim-provider-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	const source = "xrocket/package-nodes:/opt/data/xrocket/package/nodes.yaml"
	const value = "install_mode=dual; participants=2"
	resolved, err := deploymenttopology.Resolve([]deploymenttopology.Evidence{{
		ID:           "xrocket-package-nodes-00",
		Source:       source,
		Value:        value,
		Confidence:   deploymenttopology.ConfidenceHigh,
		State:        deploymenttopology.EvidenceObserved,
		TopologyKind: deploymenttopology.KindDual,
		Participants: []deploymenttopology.ParticipantFact{
			{Key: "ip:10.0.0.1", Addresses: []string{"10.0.0.1"}, Local: true},
			{Key: "ip:10.0.0.2", Addresses: []string{"10.0.0.2"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resolved = deploymenttopology.WithParticipantTrust(resolved)
	if resolved.TopologyKind != deploymenttopology.KindDual || resolved.Status != deploymenttopology.StatusConfirmed {
		t.Fatalf("provider fixture = %s/%s", resolved.TopologyKind, resolved.Status)
	}
	result := task.DeploymentTopologyResult{StartedAt: now.Add(2 * time.Second), CompletedAt: now.Add(3 * time.Second), Topology: resolved}
	if _, err := store.CompleteTask(ctx, "node-a", resource.Metadata.ID, task.ResultSubmission{
		ClaimID:                  "claim-provider-1",
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
	if loaded.DeploymentTopologyResult == nil {
		t.Fatal("provider topology result was not reconstructed")
	}
	topology := loaded.DeploymentTopologyResult.Topology
	if topology.TopologyKind != deploymenttopology.KindDual || topology.Status != deploymenttopology.StatusConfirmed {
		t.Fatalf("reopened topology = %s/%s", topology.TopologyKind, topology.Status)
	}
	if len(topology.Evidence) != 1 || topology.Evidence[0].Source != source || topology.Evidence[0].Value != value {
		t.Fatalf("provider evidence attribution was not durable: %#v", topology.Evidence)
	}
	if len(topology.Participants) != 2 || !topology.Participants[0].Local && !topology.Participants[1].Local {
		t.Fatalf("provider participant attribution was not durable: %#v", topology.Participants)
	}
}
