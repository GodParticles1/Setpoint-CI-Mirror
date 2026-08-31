package agent

import (
	"os"
	"strings"
	"testing"
)

func TestGenericOperationRunnerDoesNotExposeClickHouseLedgerTypes(t *testing.T) {
	source, err := os.ReadFile("operation_execution.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"operation/clickhouse", "clickhouse.LedgerStore", "OperationExecutionDefinitionFactory"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("generic operation runner leaks capability-specific contract %q", forbidden)
		}
	}
}
