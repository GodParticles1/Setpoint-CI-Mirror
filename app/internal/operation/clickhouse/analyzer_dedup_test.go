package clickhouse

import "testing"

func TestBuildExchangeExecutionPlanDeduplicatesAliasesToSamePhysicalTarget(t *testing.T) {
	analysis := PairAnalysis{
		Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events_a", "events_b"}},
		Source: Snapshot{Database: "db", Requested: []string{"events_a", "events_b"}, Tables: []Table{{Database: "db", Name: "events_a", Engine: "Distributed", IsDistributed: true, WriteTarget: &WriteTarget{Database: "db", Table: "events_local"}}, {Database: "db", Name: "events_b", Engine: "Distributed", IsDistributed: true, WriteTarget: &WriteTarget{Database: "db", Table: "events_local"}}, {Database: "db", Name: "events_local", Engine: "MergeTree"}}},
		Target: Snapshot{Database: "db", Requested: []string{"events_a", "events_b"}, Tables: []Table{{Database: "db", Name: "events_a", Engine: "Distributed", IsDistributed: true, WriteTarget: &WriteTarget{Database: "db", Table: "events_local"}}, {Database: "db", Name: "events_b", Engine: "Distributed", IsDistributed: true, WriteTarget: &WriteTarget{Database: "db", Table: "events_local"}}, {Database: "db", Name: "events_local", Engine: "MergeTree"}}},
		Precheck: PrecheckReport{Compatible: true},
	}
	plan, err := BuildExchangeExecutionPlan("run-1", analysis)
	if err != nil { t.Fatal(err) }
	if len(plan.Items) != 1 { t.Fatalf("items=%d", len(plan.Items)) }
}
