package clickhouse

import (
	"strings"
	"testing"
)

func physicalLabInput() ReplicatedPartitionPhysicalLabInput {
	source := Snapshot{
		Role:    RoleSource,
		Version: "24.8.1.1",
		Topology: Topology{
			Mode:     "single",
			Shards:   1,
			Replicas: 1,
		},
	}
	target := Snapshot{
		Role:    RoleTarget,
		Version: "24.8.1.1",
		Topology: Topology{
			Mode:         "clustered",
			ClusterNames: []string{"sp_lab_cluster"},
			Shards:       1,
			Replicas:     3,
		},
		Clusters: []ClusterMember{
			{Cluster: "sp_lab_cluster", ShardNum: 1, ReplicaNum: 1, HostAddress: "10.0.0.1", Port: 9000},
			{Cluster: "sp_lab_cluster", ShardNum: 1, ReplicaNum: 2, HostAddress: "10.0.0.2", Port: 9000},
			{Cluster: "sp_lab_cluster", ShardNum: 1, ReplicaNum: 3, HostAddress: "10.0.0.3", Port: 9000},
		},
	}
	return ReplicatedPartitionPhysicalLabInput{
		BaseRunID:      "lab-20260807-01",
		SourceEndpoint: Endpoint{Host: "10.0.1.1", Port: 9000},
		TargetEndpoint: Endpoint{Host: "10.0.0.1", Port: 9000},
		SourceSnapshot: source,
		TargetSnapshot: target,
		Partition:      "202608",
	}
}

func TestPhysicalLabManifestFreezesIsolationAndKeepsProductApplyDisabled(t *testing.T) {
	manifest, err := BuildReplicatedPartitionPhysicalLabManifest(physicalLabInput())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProductApplyEnabled {
		t.Fatal("physical lab manifest unexpectedly enables product Apply")
	}
	if manifest.SchemaVersion != replicatedPartitionPhysicalLabSchema || manifest.ExpectedReplicas != 3 || manifest.Cluster != "sp_lab_cluster" {
		t.Fatalf("manifest=%#v", manifest)
	}
	if !strings.HasPrefix(manifest.Isolation.Database, "sp_lab_") || manifest.Isolation.SourceTable != "source_mt" || manifest.Isolation.TargetTable != "target_rmt" {
		t.Fatalf("isolation=%#v", manifest.Isolation)
	}
	if len(manifest.Scenarios) != 3 {
		t.Fatalf("scenarios=%#v", manifest.Scenarios)
	}
	seenRuns := map[string]bool{}
	seenStaging := map[string]bool{}
	for _, scenario := range manifest.Scenarios {
		if seenRuns[scenario.RunID] || seenStaging[scenario.StagingTable] {
			t.Fatalf("scenario identifiers are not unique: %#v", manifest.Scenarios)
		}
		seenRuns[scenario.RunID] = true
		seenStaging[scenario.StagingTable] = true
		if !strings.Contains(scenario.WriteAuthorization, "L3") || !strings.Contains(scenario.WriteAuthorization, "L4") {
			t.Fatalf("write authorization boundary missing: %#v", scenario)
		}
	}
}

func TestPhysicalLabManifestAllowsReplicaConvergenceAsOptionalIntermediateState(t *testing.T) {
	manifest, err := BuildReplicatedPartitionPhysicalLabManifest(physicalLabInput())
	if err != nil {
		t.Fatal(err)
	}
	baseline := manifest.Scenarios[0]
	if len(baseline.RequiredLedgerStates) != 3 || baseline.RequiredLedgerStates[0] != LedgerVerified || baseline.RequiredLedgerStates[1] != LedgerCommitPending || baseline.RequiredLedgerStates[2] != LedgerCommitted {
		t.Fatalf("baseline required states=%v", baseline.RequiredLedgerStates)
	}
	if len(baseline.AllowedIntermediateStates) != 1 || baseline.AllowedIntermediateStates[0] != LedgerReplicasConverging {
		t.Fatalf("baseline intermediate states=%v", baseline.AllowedIntermediateStates)
	}
}

func TestPhysicalLabManifestRequiresExactVersionAndProvenSingleSource(t *testing.T) {
	input := physicalLabInput()
	input.TargetSnapshot.Version = "24.8.1.2"
	if _, err := BuildReplicatedPartitionPhysicalLabManifest(input); err == nil {
		t.Fatal("version mismatch unexpectedly accepted")
	}
	input = physicalLabInput()
	input.SourceSnapshot.Topology.Mode = "clustered"
	if _, err := BuildReplicatedPartitionPhysicalLabManifest(input); err == nil {
		t.Fatal("clustered source unexpectedly accepted")
	}
	input = physicalLabInput()
	input.SourceSnapshot.Topology = Topology{}
	if _, err := BuildReplicatedPartitionPhysicalLabManifest(input); err == nil {
		t.Fatal("unproven source topology unexpectedly accepted")
	}
}

func TestPhysicalLabManifestRejectsMultiShardTarget(t *testing.T) {
	input := physicalLabInput()
	input.TargetSnapshot.Topology.Shards = 2
	input.TargetSnapshot.Clusters = append(input.TargetSnapshot.Clusters,
		ClusterMember{Cluster: "sp_lab_cluster", ShardNum: 2, ReplicaNum: 1, HostAddress: "10.0.0.4", Port: 9000})
	if _, err := BuildReplicatedPartitionPhysicalLabManifest(input); err == nil {
		t.Fatal("multi-shard target unexpectedly accepted")
	}
}

func TestPhysicalLabManifestDerivesClusterNameWithoutTopologyClusterNames(t *testing.T) {
	input := physicalLabInput()
	input.TargetSnapshot.Topology.ClusterNames = nil
	manifest, err := BuildReplicatedPartitionPhysicalLabManifest(input)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Cluster != "sp_lab_cluster" {
		t.Fatalf("cluster=%q", manifest.Cluster)
	}
}

func TestPhysicalLabIsolationIsDeterministicButRunSpecific(t *testing.T) {
	one, err := buildReplicatedPartitionLabIsolation("lab-a")
	if err != nil {
		t.Fatal(err)
	}
	two, err := buildReplicatedPartitionLabIsolation("lab-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := buildReplicatedPartitionLabIsolation("lab-b")
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatalf("same run ID generated different isolation: %#v %#v", one, two)
	}
	if one.Database == other.Database || one.OwnershipToken == other.OwnershipToken {
		t.Fatalf("different run IDs collided: %#v %#v", one, other)
	}
}

func TestPhysicalLabFaultScenariosNeverRequireServiceOrNetworkDisruption(t *testing.T) {
	manifest, err := BuildReplicatedPartitionPhysicalLabManifest(physicalLabInput())
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := manifest.Scenarios[1]
	if ambiguous.Scenario != LabScenarioAmbiguousCommit || !strings.Contains(ambiguous.FaultInjection, "process-local") || !strings.Contains(ambiguous.FaultInjection, "do not stop ClickHouse") {
		t.Fatalf("ambiguous scenario=%#v", ambiguous)
	}
	drift := manifest.Scenarios[2]
	if drift.Scenario != LabScenarioRollbackDriftGuard || !strings.Contains(drift.FaultInjection, "explicit fault-injection authorization") {
		t.Fatalf("drift scenario=%#v", drift)
	}
}
