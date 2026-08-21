package clickhouse

import "testing"

func TestPrecheckAllowsMergeTreeToReplicatedMergeTreeWhenTargetEmpty(t *testing.T) {
	columns := []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	source := Snapshot{
		Role: RoleSource, Version: "24.8.1.1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true},
		Topology: Topology{Shards: 1, Replicas: 1},
		Tables: []Table{{
			Database: "db", Name: "events", Engine: "MergeTree",
			PartitionKey: "toYYYYMM(ts)", SortingKey: "id", PrimaryKey: "id", Columns: columns,
			Partitions: []Partition{{Partition: "202608", Rows: 10, BytesOnDisk: 1000}},
		}},
	}
	target := Snapshot{
		Role: RoleTarget, Version: "24.8.1.1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Replicas: true, Mutations: true, Disks: true},
		Topology: Topology{Shards: 1, Replicas: 3},
		Tables: []Table{{
			Database: "db", Name: "events", Engine: "ReplicatedMergeTree", IsReplicated: true,
			PartitionKey: "toYYYYMM(ts)", SortingKey: "id", PrimaryKey: "id", Columns: columns,
		}},
		Replicas: []Replica{{Database: "db", Table: "events", ReplicaName: "r1"}},
		Disks:    []Disk{{Name: "default", FreeSpace: 100000, TotalSpace: 200000}},
	}
	report := NewPrechecker().Check(source, target)
	if !report.Compatible {
		t.Fatalf("expected compatible, issues=%#v", report.Issues)
	}
	if report.EstimatedBytes != 1000 {
		t.Fatalf("estimated=%d", report.EstimatedBytes)
	}
}

func TestPrecheckAllowsSupportedNonEmptyTargetWithRestoreRequirement(t *testing.T) {
	columns := []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	source := Snapshot{
		Role: RoleSource, Version: "1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true},
		Topology: Topology{Mode: "single", Shards: 1, Replicas: 1},
		Tables:   []Table{{Database: "db", Name: "events", Engine: "MergeTree", Columns: columns, Partitions: []Partition{{Rows: 10, BytesOnDisk: 100}}}},
	}
	target := Snapshot{
		Role: RoleTarget, Version: "1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true, Disks: true},
		Topology: Topology{Mode: "single", Shards: 1, Replicas: 1},
		Tables:   []Table{{Database: "db", Name: "events", Engine: "MergeTree", Columns: columns, Partitions: []Partition{{Rows: 1, BytesOnDisk: 10}}}},
		Disks:    []Disk{{FreeSpace: 10000}},
	}
	report := NewPrechecker().Check(source, target)
	if !report.Compatible {
		t.Fatalf("supported non-empty target blocked: %#v", report.Issues)
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "TARGET_NONEMPTY_RESTORE_REQUIRED" && issue.Severity == "warning" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing TARGET_NONEMPTY_RESTORE_REQUIRED: %#v", report.Issues)
	}
}

func TestPrecheckBlocksUnsupportedNonEmptySourceAndTopology(t *testing.T) {
	columns := []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	baseSource := Snapshot{
		Role: RoleSource, Version: "1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true},
		Topology: Topology{Mode: "single", Shards: 1, Replicas: 1},
		Tables:   []Table{{Database: "db", Name: "events", Engine: "MergeTree", EngineFull: "MergeTree()", StoragePolicy: "default", Columns: columns, Partitions: []Partition{{Rows: 10, BytesOnDisk: 100}}}},
	}
	baseTarget := Snapshot{
		Role: RoleTarget, Version: "1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true, Disks: true},
		Topology: Topology{Mode: "single", Shards: 1, Replicas: 1},
		Tables:   []Table{{Database: "db", Name: "events", Engine: "MergeTree", EngineFull: "MergeTree()", StoragePolicy: "default", Columns: columns, Partitions: []Partition{{Rows: 1, BytesOnDisk: 10}}}},
		Disks:    []Disk{{FreeSpace: 10000}},
	}

	tests := []struct {
		name   string
		mutate func(*Snapshot, *Snapshot)
		code   string
	}{
		{
			name: "replicated source",
			mutate: func(source, _ *Snapshot) {
				source.Tables[0].Engine = "ReplicatedMergeTree"
				source.Tables[0].IsReplicated = true
			},
			code: "NONEMPTY_REPLICATED_UNSUPPORTED",
		},
		{
			name: "multi-node source topology",
			mutate: func(source, _ *Snapshot) {
				source.Topology = Topology{Mode: "clustered", Shards: 1, Replicas: 2}
			},
			code: "NONEMPTY_MULTI_NODE_UNSUPPORTED",
		},
		{
			name: "unknown target topology",
			mutate: func(_, target *Snapshot) {
				target.Topology = Topology{}
			},
			code: "NONEMPTY_MULTI_NODE_UNSUPPORTED",
		},
		{
			name: "engine definition mismatch",
			mutate: func(_, target *Snapshot) {
				target.Tables[0].EngineFull = "MergeTree() SETTINGS index_granularity=4096"
			},
			code: "NONEMPTY_ENGINE_DEFINITION_MISMATCH",
		},
		{
			name: "engine quoted whitespace mismatch",
			mutate: func(source, target *Snapshot) {
				source.Tables[0].EngineFull = "MergeTree() SETTINGS storage_policy='cold archive'"
				target.Tables[0].EngineFull = "MergeTree() SETTINGS storage_policy='coldarchive'"
			},
			code: "NONEMPTY_ENGINE_DEFINITION_MISMATCH",
		},
		{
			name: "storage policy mismatch",
			mutate: func(_, target *Snapshot) {
				target.Tables[0].StoragePolicy = "cold"
			},
			code: "NONEMPTY_STORAGE_POLICY_MISMATCH",
		},
		{
			name: "multiple requested tables",
			mutate: func(source, target *Snapshot) {
				source.Requested = append(source.Requested, "metrics")
				target.Requested = append(target.Requested, "metrics")
				sourceTable := source.Tables[0]
				sourceTable.Name = "metrics"
				targetTable := target.Tables[0]
				targetTable.Name = "metrics"
				source.Tables = append(source.Tables, sourceTable)
				target.Tables = append(target.Tables, targetTable)
			},
			code: "NONEMPTY_MULTI_TABLE_UNSUPPORTED",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, target := baseSource, baseTarget
			source.Tables = append([]Table(nil), baseSource.Tables...)
			target.Tables = append([]Table(nil), baseTarget.Tables...)
			test.mutate(&source, &target)
			report := NewPrechecker().Check(source, target)
			if report.Compatible || !hasBlockingIssue(report, test.code) {
				t.Fatalf("expected blocking %s, report=%#v", test.code, report)
			}
		})
	}
}

func hasBlockingIssue(report PrecheckReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code && issue.Severity == "blocking" {
			return true
		}
	}
	return false
}

func TestPrecheckBlocksUnsupportedReplicatedNonEmptyTarget(t *testing.T) {
	columns := []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	source := Snapshot{
		Role: RoleSource, Version: "1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true},
		Topology: Topology{Shards: 1},
		Tables:   []Table{{Database: "db", Name: "events", Engine: "MergeTree", Columns: columns, Partitions: []Partition{{Rows: 10, BytesOnDisk: 100}}}},
	}
	target := Snapshot{
		Role: RoleTarget, Version: "1", Database: "db", Requested: []string{"events"},
		Checks:   DiscoveryChecks{Tables: true, Columns: true, Parts: true, Mutations: true, Disks: true, Replicas: true},
		Topology: Topology{Shards: 1, Replicas: 1},
		Tables:   []Table{{Database: "db", Name: "events", Engine: "ReplicatedMergeTree", IsReplicated: true, Columns: columns, Partitions: []Partition{{Rows: 1, BytesOnDisk: 10}}}},
		Replicas: []Replica{{Database: "db", Table: "events", ReplicaName: "r1"}},
		Disks:    []Disk{{FreeSpace: 10000}},
	}
	report := NewPrechecker().Check(source, target)
	if report.Compatible {
		t.Fatalf("replicated non-empty target unexpectedly compatible: %#v", report.Issues)
	}
	for _, issue := range report.Issues {
		if issue.Code == "NONEMPTY_REPLICATED_UNSUPPORTED" && issue.Severity == "blocking" {
			return
		}
	}
	t.Fatalf("missing NONEMPTY_REPLICATED_UNSUPPORTED: %#v", report.Issues)
}
