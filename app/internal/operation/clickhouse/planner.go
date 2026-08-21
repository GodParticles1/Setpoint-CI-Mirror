package clickhouse

import "sort"

type Planner struct {
	selector *StrategySelector
}

func NewPlanner() *Planner { return &Planner{selector: NewStrategySelector()} }

func (planner *Planner) Build(parameters Parameters, source, target Snapshot, precheck PrecheckReport) AnalysisPlan {
	plan := AnalysisPlan{SchemaVersion: "clickhouse.analysis_plan.v1", SafetyApproved: precheck.Compatible, ImplementationReady: false, ApplyEnabled: false, EstimatedBytes: precheck.EstimatedBytes, RequiresPairPlan: true}
	dataSet := map[string]struct{}{}
	routingSet := map[string]struct{}{}
	for _, logicalName := range source.Requested {
		logical, ok := findTable(source, source.Database, logicalName)
		if !ok { continue }
		if logical.IsDistributed {
			routingSet[logical.Database+"."+logical.Name] = struct{}{}
			if physical, resolved := resolveDataTable(source, logical); resolved { dataSet[physical.Database+"."+physical.Name] = struct{}{} }
		} else if !logical.IsMaterializedView {
			dataSet[logical.Database+"."+logical.Name] = struct{}{}
		}
	}
	for value := range dataSet { plan.DataTables = append(plan.DataTables, value) }
	for value := range routingSet { plan.RoutingTables = append(plan.RoutingTables, value) }
	sort.Strings(plan.DataTables); sort.Strings(plan.RoutingTables)

	decision := planner.selector.Select(parameters, source, target, precheck)
	plan.RecommendedStrategy = string(decision.Selected)
	for _, candidate := range decision.Candidates { plan.CandidateStrategies = append(plan.CandidateStrategies, string(candidate)) }
	plan.Reason = decision.Reason
	if !precheck.Compatible {
		plan.ApplyEnabled = false
		return plan
	}

	// A strategy can be selected for planning without being executable. Apply
	// stays disabled until a durable ledger, verified restore-point provider,
	// transport implementation and runtime-only SecretRef delivery path are all
	// wired and tested end-to-end.
	plan.ImplementationReady = false
	plan.ApplyEnabled = false
	return plan
}
