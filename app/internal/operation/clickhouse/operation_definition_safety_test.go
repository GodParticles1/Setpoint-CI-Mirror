package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

type definitionCapabilityClient struct {
	databaseEngine string
	tableEngine    string
	exchangeErr    error
}

func (client definitionCapabilityClient) Query(_ context.Context, request QueryRequest) (string, error) {
	switch {
	case strings.Contains(request.Query, "FROM system.databases"):
		return client.databaseEngine, nil
	case strings.Contains(request.Query, "FROM system.tables"):
		return client.tableEngine, nil
	case strings.HasPrefix(request.Query, "EXPLAIN SYNTAX EXCHANGE TABLES"):
		if client.exchangeErr != nil {
			return "", client.exchangeErr
		}
		return strings.TrimPrefix(request.Query, "EXPLAIN SYNTAX "), nil
	default:
		return "", nil
	}
}

func executablePairSnapshot(tableEngine string) PairSnapshot {
	columns := []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	checks := DiscoveryChecks{Tables: true, Columns: true, Parts: true, Replicas: true, Mutations: true, Disks: true}
	source := Snapshot{
		Role: RoleSource, Version: "24.8.1.1", Database: "db", Requested: []string{"events"}, Effective: []string{"events"}, Checks: checks,
		Tables: []Table{{Database: "db", Name: "events", Engine: "MergeTree", Columns: columns, Partitions: []Partition{{Partition: "202608", Rows: 10, BytesOnDisk: 100}}}},
		Disks: []Disk{{Name: "default", FreeSpace: 10000, TotalSpace: 20000}},
	}
	target := Snapshot{
		Role: RoleTarget, Version: "24.8.1.1", Database: "db", Requested: []string{"events"}, Effective: []string{"events"}, Checks: checks,
		Tables: []Table{{Database: "db", Name: "events", Engine: tableEngine, IsReplicated: strings.HasPrefix(tableEngine, "Replicated"), Columns: columns}},
		Disks: []Disk{{Name: "default", FreeSpace: 10000, TotalSpace: 20000}},
	}
	return PairSnapshot{Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}, Source: source, Target: target}
}

func TestDefinitionPrecheckBlocksServerWithoutExchangeParser(t *testing.T) {
	client := definitionCapabilityClient{databaseEngine: "Atomic", tableEngine: "MergeTree", exchangeErr: &executor.Error{Kind: executor.ErrorExit, Result: executor.Result{ExitCode: 62}, Err: errors.New("exit status 62")}}
	definition, err := NewDefinition(client, newMemoryLedger(), &noOpStaging{}, definitionNativeTransport{}, &restoreVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := encodePairSnapshot(executablePairSnapshot("MergeTree"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := definition.Precheck(context.Background(), operation.PrecheckInput{Discovery: operation.Discovery{Snapshot: artifact}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatal("server without an EXCHANGE parser unexpectedly passed precheck")
	}
	for _, finding := range report.Findings {
		if finding.Code == "ATOMIC_EXCHANGE_NOT_AVAILABLE" && finding.Severity == operation.FindingBlocking && strings.Contains(finding.Detail, "exit code 62") {
			return
		}
	}
	t.Fatalf("missing EXCHANGE parser blocking finding: %#v", report.Findings)
}

func TestDefinitionPrecheckBlocksReplicatedTargetExecution(t *testing.T) {
	client := definitionCapabilityClient{databaseEngine: "Atomic", tableEngine: "ReplicatedMergeTree"}
	definition, err := NewDefinition(client, newMemoryLedger(), &noOpStaging{}, definitionNativeTransport{}, &restoreVerifier{})
	if err != nil { t.Fatal(err) }
	artifact, err := encodePairSnapshot(executablePairSnapshot("ReplicatedMergeTree"))
	if err != nil { t.Fatal(err) }
	report, err := definition.Precheck(context.Background(), operation.PrecheckInput{Discovery: operation.Discovery{Snapshot: artifact}})
	if err != nil { t.Fatal(err) }
	if report.Passed { t.Fatal("ReplicatedMergeTree target unexpectedly passed executable operation precheck") }
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "ATOMIC_EXCHANGE_NOT_AVAILABLE" && finding.Severity == operation.FindingBlocking { found = true }
	}
	if !found { t.Fatalf("missing executable adapter blocking finding: %#v", report.Findings) }
}

func TestDefinitionPrecheckBlocksTimeRangeUntilSafeCommitAdapterExists(t *testing.T) {
	client := definitionCapabilityClient{databaseEngine: "Atomic", tableEngine: "MergeTree"}
	definition, err := NewDefinition(client, newMemoryLedger(), &noOpStaging{}, definitionNativeTransport{}, &restoreVerifier{})
	if err != nil { t.Fatal(err) }
	snapshot := executablePairSnapshot("MergeTree")
	snapshot.Pair.TimeColumn = "event_time"
	snapshot.Pair.StartTime = "2026-08-01T00:00:00Z"
	snapshot.Pair.EndTime = "2026-08-02T00:00:00Z"
	artifact, err := encodePairSnapshot(snapshot)
	if err != nil { t.Fatal(err) }
	report, err := definition.Precheck(context.Background(), operation.PrecheckInput{Discovery: operation.Discovery{Snapshot: artifact}})
	if err != nil { t.Fatal(err) }
	if report.Passed { t.Fatal("time-bounded Apply unexpectedly passed current whole-table commit precheck") }
	found := false
	for _, finding := range report.Findings {
		if finding.Code == "TIME_RANGE_EXECUTION_NOT_READY" && finding.Severity == operation.FindingBlocking { found = true }
	}
	if !found { t.Fatalf("missing time range blocking finding: %#v", report.Findings) }
}
