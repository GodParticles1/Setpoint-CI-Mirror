package clickhouse

import (
	"context"
	"testing"

	"setpoint/internal/operation"
)

func TestMigrationProductContractDoesNotExposeExecutionStrategy(t *testing.T) {
	metadata := OperationMetadata()
	if metadata.ID != OperationID {
		t.Fatalf("metadata operation id=%q want=%q", metadata.ID, OperationID)
	}

	required := map[string]bool{
		"source":   false,
		"target":   false,
		"database": false,
		"tables":   false,
	}
	for _, parameter := range metadata.Parameters {
		if parameter.Name == "strategy" {
			t.Fatal("ClickHouse migration product contract must not ask the user to choose an execution strategy")
		}
		if _, ok := required[parameter.Name]; ok {
			required[parameter.Name] = parameter.Required
		}
	}
	for name, markedRequired := range required {
		if !markedRequired {
			t.Fatalf("required migration intent parameter %q is missing or not required", name)
		}
	}
}

func TestDefinitionPrecheckAllowsGuardedWholeTableAtomicMergeTreeSlice(t *testing.T) {
	client := definitionCapabilityClient{databaseEngine: "Atomic", tableEngine: "MergeTree"}
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
	if !report.Passed {
		t.Fatalf("guarded whole-table Atomic MergeTree slice should be executable, findings=%#v", report.Findings)
	}
	for _, finding := range report.Findings {
		if finding.Severity == operation.FindingBlocking {
			t.Fatalf("supported whole-table slice returned blocking finding %#v", finding)
		}
	}
}
