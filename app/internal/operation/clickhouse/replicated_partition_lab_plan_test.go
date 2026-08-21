package clickhouse

import (
	"strings"
	"testing"
)

func TestReplicatedPartitionLabPlanIsExplicitlyNonExecutable(t *testing.T) {
	plan, err := BuildReplicatedPartitionLabPlan("run-42", "db", replicatedTargetTable(), "202608", "default")
	if err != nil { t.Fatal(err) }
	if plan.ApplyEnabled { t.Fatal("lab plan unexpectedly enabled Apply") }
	if !strings.Contains(plan.CreateStagingSQL, "ENGINE = MergeTree") || strings.Contains(plan.CreateStagingSQL, "Replicated") { t.Fatalf("staging=%q", plan.CreateStagingSQL) }
	if !strings.Contains(plan.CommitSQL, "REPLACE PARTITION '202608'") { t.Fatalf("commit=%q", plan.CommitSQL) }
	if !strings.Contains(plan.RollbackSQL, "DROP PARTITION '202608'") { t.Fatalf("rollback=%q", plan.RollbackSQL) }
	if len(plan.Preconditions) < 5 || len(plan.Postconditions) < 4 { t.Fatalf("plan lacks safety gates: %#v", plan) }
}

func TestReplicatedPartitionLabPlanUsesRunOwnedStagingName(t *testing.T) {
	one, err := BuildReplicatedPartitionLabPlan("run-a", "db", replicatedTargetTable(), "202608", "default")
	if err != nil { t.Fatal(err) }
	two, err := BuildReplicatedPartitionLabPlan("run-b", "db", replicatedTargetTable(), "202608", "default")
	if err != nil { t.Fatal(err) }
	if one.StagingTable == two.StagingTable { t.Fatalf("staging collision: %q", one.StagingTable) }
}
