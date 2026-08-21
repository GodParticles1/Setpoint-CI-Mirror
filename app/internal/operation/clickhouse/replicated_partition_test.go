package clickhouse

import (
	"context"
	"strings"
	"testing"
)

type replicatedPartitionClient struct {
	storagePolicy string
	enforce       string
}
func (client replicatedPartitionClient) Query(_ context.Context, request QueryRequest) (string, error) {
	if strings.Contains(request.Query, "SELECT storage_policy FROM system.tables") { return client.storagePolicy, nil }
	if strings.Contains(request.Query, "enforce_index_structure_match_on_partition_manipulation") { return client.enforce, nil }
	return "", nil
}

func replicatedTargetTable() Table {
	return Table{Database: "db", Name: "events_local", Engine: "ReplicatedMergeTree", IsReplicated: true, PartitionKey: "toYYYYMM(event_time)", SortingKey: "(tenant_id,event_time,id)", PrimaryKey: "(tenant_id,event_time,id)"}
}

func TestReplicatedPartitionCapabilityIsLabOnlyEvenWhenStructureLooksEligible(t *testing.T) {
	capability, err := InspectReplicatedPartitionCapability(context.Background(), replicatedPartitionClient{storagePolicy: "default", enforce: "0"}, Endpoint{Host: "target", Port: 9000}, "db", replicatedTargetTable())
	if err != nil { t.Fatal(err) }
	if !capability.ReadyForLab { t.Fatalf("capability=%#v", capability) }
	if !capability.EnforceIndexMatchKnown { t.Fatalf("capability=%#v", capability) }
	if capability.ApplyEnabled { t.Fatal("ReplicatedMergeTree partition Apply must remain disabled before physical validation") }
}

func TestReplicatedPartitionCapabilityBlocksExactIndexParityRequirement(t *testing.T) {
	capability, err := InspectReplicatedPartitionCapability(context.Background(), replicatedPartitionClient{storagePolicy: "default", enforce: "1"}, Endpoint{Host: "target", Port: 9000}, "db", replicatedTargetTable())
	if err != nil { t.Fatal(err) }
	if capability.ReadyForLab { t.Fatalf("capability=%#v", capability) }
	if !strings.Contains(capability.Reason, "index/projection") { t.Fatalf("reason=%q", capability.Reason) }
}

func TestReplicatedPartitionCapabilityDoesNotTreatMissingSettingAsDisabled(t *testing.T) {
	capability, err := InspectReplicatedPartitionCapability(context.Background(), replicatedPartitionClient{storagePolicy: "default", enforce: ""}, Endpoint{Host: "target", Port: 9000}, "db", replicatedTargetTable())
	if err != nil { t.Fatal(err) }
	if capability.EnforceIndexMatchKnown || capability.ReadyForLab {
		t.Fatalf("capability=%#v", capability)
	}
	if !strings.Contains(capability.Reason, "absent or unavailable") {
		t.Fatalf("reason=%q", capability.Reason)
	}
}

func TestReplicatedPartitionStagingDDLDoesNotCopyReplicationIdentity(t *testing.T) {
	query, err := BuildReplicatedPartitionStagingDDL("db", "spmig_events_abc", "events_local", replicatedTargetTable(), "default")
	if err != nil { t.Fatal(err) }
	if strings.Contains(query, "Replicated") || strings.Contains(query, "zookeeper") { t.Fatalf("unsafe staging DDL=%q", query) }
	for _, want := range []string{"ENGINE = MergeTree", "PARTITION BY toYYYYMM(event_time)", "ORDER BY (tenant_id,event_time,id)", "storage_policy = 'default'"} {
		if !strings.Contains(query, want) { t.Fatalf("DDL missing %q: %s", want, query) }
	}
}

func TestReplacePartitionSQLUsesLiteralPartitionAndRunOwnedStaging(t *testing.T) {
	query, err := BuildReplacePartitionSQL("db", "events_local", "spmig_events_abc", "202608")
	if err != nil { t.Fatal(err) }
	want := "ALTER TABLE `db`.`events_local` REPLACE PARTITION '202608' FROM `db`.`spmig_events_abc`"
	if query != want { t.Fatalf("query=%q", query) }
}

func TestReplicatedPartitionDDLRejectsUntrustedDiscoveredExpression(t *testing.T) {
	table := replicatedTargetTable()
	table.PartitionKey = "toYYYYMM(event_time); DROP TABLE db.events_local"
	if _, err := BuildReplicatedPartitionStagingDDL("db", "spmig_events_abc", "events_local", table, "default"); err == nil {
		t.Fatal("unsafe discovered expression unexpectedly accepted")
	}
}
