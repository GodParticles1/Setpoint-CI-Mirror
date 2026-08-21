package clickhouse

import (
	"context"
	"strings"
	"testing"
)

func replicatedPreflightFixture(t *testing.T) (*ReplicatedPartitionLabPreflightService, PairParameters, Snapshot, Snapshot, Table, Table, *replicaLabState) {
	t.Helper()
	sourceFingerprint := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	state := newReplicaLabState(sourceFingerprint)
	state.set("source", sourceFingerprint, 1)
	client := &replicaLabClient{state: state}
	service, err := NewReplicatedPartitionLabPreflightService(client, &replicaLabVerifier{state: state})
	if err != nil {
		t.Fatal(err)
	}
	columns := []Column{{Name: "event_time", Position: 1, Type: "DateTime"}, {Name: "id", Position: 2, Type: "UInt64"}}
	sourceTable := Table{Database: "db", Name: "events", Engine: "MergeTree", PartitionKey: "toYYYYMM(event_time)", SortingKey: "(event_time,id)", PrimaryKey: "(event_time,id)", Columns: columns, Partitions: []Partition{{Partition: "202608", Rows: 10, BytesOnDisk: 1024, ActiveParts: 1}}}
	targetTable := replicatedTargetTable()
	targetTable.Name = "events"
	targetTable.PartitionKey = sourceTable.PartitionKey
	targetTable.SortingKey = sourceTable.SortingKey
	targetTable.PrimaryKey = sourceTable.PrimaryKey
	targetTable.Columns = columns
	pair := PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "r1", Port: 9000}, Database: "db", Tables: []string{"events"}}
	sourceSnapshot := Snapshot{Role: RoleSource, Database: "db", Tables: []Table{sourceTable}}
	targetSnapshot := replicaLabSnapshot()
	targetSnapshot.Database = "db"
	targetSnapshot.Tables = []Table{targetTable}
	return service, pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, state
}

func TestReplicatedPartitionPreflightPassesOnlyForEmptyHealthyTargetReplicas(t *testing.T) {
	service, pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, _ := replicatedPreflightFixture(t)
	report, err := service.Check(context.Background(), pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, "202608")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.SourceFingerprint.Rows != 10 || report.SourceBytes != 1024 || report.TargetReplicas.Absent != 3 {
		t.Fatalf("report=%#v", report)
	}
}

func TestReplicatedPartitionPreflightBlocksExistingTargetData(t *testing.T) {
	service, pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, state := replicatedPreflightFixture(t)
	state.set("r3", DataFingerprint{Rows: 1, HashSum64: "9", HashXor64: "9"}, 1)
	report, err := service.Check(context.Background(), pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, "202608")
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || report.TargetReplicas.State != ReplicaPartitionConflict {
		t.Fatalf("report=%#v", report)
	}
}

func TestReplicatedPartitionPreflightBlocksUnfinishedMutation(t *testing.T) {
	service, pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, _ := replicatedPreflightFixture(t)
	targetSnapshot.Mutations = []Mutation{{Database: "db", Table: "events", MutationID: "mutation_1", IsDone: false}}
	report, err := service.Check(context.Background(), pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, "202608")
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || len(report.Findings) == 0 || !strings.Contains(strings.Join(report.Findings, " "), "unfinished mutation") {
		t.Fatalf("report=%#v", report)
	}
}

func TestReplicatedPartitionPreflightBlocksKeyMismatch(t *testing.T) {
	service, pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, _ := replicatedPreflightFixture(t)
	targetTable.SortingKey = "id"
	targetSnapshot.Tables[0] = targetTable
	report, err := service.Check(context.Background(), pair, sourceSnapshot, targetSnapshot, sourceTable, targetTable, "202608")
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed || !strings.Contains(strings.Join(report.Findings, " "), "sorting keys are different") {
		t.Fatalf("report=%#v", report)
	}
}
