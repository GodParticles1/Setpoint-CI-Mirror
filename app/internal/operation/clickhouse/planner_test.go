package clickhouse

import "testing"

func TestPlannerKeepsApplyDisabledUntilExecutionSliceExists(t *testing.T) {
	source := Snapshot{Database: "db", Requested: []string{"events"}, Tables: []Table{{Database: "db", Name: "events", Engine: "MergeTree"}}}
	plan := NewPlanner().Build(Parameters{Database: "db"}, source, Snapshot{}, PrecheckReport{Compatible: true, EstimatedBytes: 42})
	if !plan.SafetyApproved {
		t.Fatal("safety should be approved")
	}
	if plan.ImplementationReady || plan.ApplyEnabled {
		t.Fatalf("apply must remain disabled: %#v", plan)
	}
	if plan.RecommendedStrategy != string(StrategyNativeStream) {
		t.Fatalf("strategy=%q", plan.RecommendedStrategy)
	}
	if len(plan.DataTables) != 1 || plan.DataTables[0] != "db.events" {
		t.Fatalf("data tables=%v", plan.DataTables)
	}
}
