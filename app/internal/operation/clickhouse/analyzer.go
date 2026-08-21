package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type PairAnalysis struct {
	Pair     PairParameters `json:"pair"`
	Source   Snapshot       `json:"source"`
	Target   Snapshot       `json:"target"`
	Precheck PrecheckReport `json:"precheck"`
	Plan     AnalysisPlan   `json:"plan"`
}

type PairAnalyzer struct {
	discovery *DiscoveryService
	precheck  *Prechecker
	planner   *Planner
}

func NewPairAnalyzer(discovery *DiscoveryService, precheck *Prechecker, planner *Planner) (*PairAnalyzer, error) {
	if discovery == nil || precheck == nil || planner == nil {
		return nil, errors.New("discovery, prechecker and planner are required")
	}
	return &PairAnalyzer{discovery: discovery, precheck: precheck, planner: planner}, nil
}

func (analyzer *PairAnalyzer) Analyze(ctx context.Context, pair PairParameters) (PairAnalysis, error) {
	pair, err := normalizePairParameters(pair)
	if err != nil {
		return PairAnalysis{}, err
	}
	source, err := analyzer.discovery.Discover(ctx, pair.parametersFor(RoleSource))
	if err != nil {
		return PairAnalysis{}, fmt.Errorf("discover source ClickHouse: %w", err)
	}
	target, err := analyzer.discovery.Discover(ctx, pair.parametersFor(RoleTarget))
	if err != nil {
		return PairAnalysis{}, fmt.Errorf("discover target ClickHouse: %w", err)
	}
	report := analyzer.precheck.Check(source, target)
	plan := analyzer.planner.Build(pair.parametersFor(RoleSource), source, target, report)
	return PairAnalysis{Pair: pair, Source: source, Target: target, Precheck: report, Plan: plan}, nil
}

func BuildExchangeExecutionPlan(runID string, analysis PairAnalysis) (ExchangeRestorePlan, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ExchangeRestorePlan{}, errors.New("run ID is required")
	}
	if !analysis.Precheck.Compatible {
		return ExchangeRestorePlan{}, errors.New("cannot build execution plan while precheck has blocking findings")
	}
	pair, err := normalizePairParameters(analysis.Pair)
	if err != nil {
		return ExchangeRestorePlan{}, err
	}
	filter, err := pair.timeFilter()
	if err != nil {
		return ExchangeRestorePlan{}, err
	}
	plan := ExchangeRestorePlan{Pair: pair}
	seenTargets := make(map[string]string)
	for _, logicalName := range analysis.Source.Requested {
		sourceLogical, sourceOK := findTable(analysis.Source, analysis.Source.Database, logicalName)
		targetLogical, targetOK := findTable(analysis.Target, analysis.Target.Database, logicalName)
		if !sourceOK || !targetOK {
			return ExchangeRestorePlan{}, fmt.Errorf("requested table %q disappeared after precheck", logicalName)
		}
		sourceData, sourceOK := resolveDataTable(analysis.Source, sourceLogical)
		targetData, targetOK := resolveDataTable(analysis.Target, targetLogical)
		if !sourceOK || !targetOK {
			return ExchangeRestorePlan{}, fmt.Errorf("physical data table for %q cannot be resolved", logicalName)
		}
		targetKey := targetData.Database + "." + targetData.Name
		sourceKey := sourceData.Database + "." + sourceData.Name
		if previousSource, exists := seenTargets[targetKey]; exists {
			if previousSource != sourceKey {
				return ExchangeRestorePlan{}, fmt.Errorf("multiple source tables map to target %s", targetKey)
			}
			continue
		}
		seenTargets[targetKey] = sourceKey
		staging, err := BuildStagingTableName(runID, targetData.Name)
		if err != nil {
			return ExchangeRestorePlan{}, err
		}
		chunk := TransferChunk{RunID: runID, Strategy: StrategyNativeStream, SourceDatabase: sourceData.Database, SourceTable: sourceData.Name, TargetDatabase: targetData.Database, TargetTable: targetData.Name, StagingTable: staging, Filter: filter, Sequence: uint64(len(plan.Items) + 1)}
		plan.Items = append(plan.Items, ExchangeRestoreItem{Chunk: chunk, TargetTable: targetData})
	}
	if len(plan.Items) == 0 {
		return ExchangeRestorePlan{}, errors.New("execution plan contains no ClickHouse data tables")
	}
	return plan, nil
}
