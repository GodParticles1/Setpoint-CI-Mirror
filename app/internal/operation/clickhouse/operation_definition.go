package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"setpoint/internal/operation"
)

const pairSnapshotSchema = "clickhouse.pair_snapshot.v1"
const applyStateSchema = "clickhouse.apply_state.v1"

type PairSnapshot struct {
	Pair   PairParameters `json:"pair"`
	Source Snapshot       `json:"source"`
	Target Snapshot       `json:"target"`
}

type ApplyState struct {
	RunID     string      `json:"run_id"`
	Committed []LedgerKey `json:"committed"`
}

type Definition struct {
	*PlanningDefinition
	ledger    LedgerStore
	staging   StagingController
	transport NativeTransport
	verifier  FingerprintVerifier
}

type PlanningDefinition struct {
	client     QueryClient
	discovery  *DiscoveryService
	prechecker *Prechecker
	planner    *Planner
}

func NewPlanningDefinition(client QueryClient) (*PlanningDefinition, error) {
	if client == nil {
		return nil, errors.New("ClickHouse planning definition requires a query client")
	}
	discovery, err := NewDiscoveryService(client)
	if err != nil {
		return nil, err
	}
	return &PlanningDefinition{client: client, discovery: discovery, prechecker: NewPrechecker(), planner: NewPlanner()}, nil
}

func NewDefinition(client QueryClient, ledger LedgerStore, staging StagingController, transport NativeTransport, verifier FingerprintVerifier) (*Definition, error) {
	if client == nil || ledger == nil || staging == nil || transport == nil || verifier == nil {
		return nil, errors.New("ClickHouse definition requires query client, ledger, staging, transport and verifier")
	}
	planning, err := NewPlanningDefinition(client)
	if err != nil {
		return nil, err
	}
	return &Definition{PlanningDefinition: planning, ledger: ledger, staging: staging, transport: transport, verifier: verifier}, nil
}

func (definition *PlanningDefinition) Metadata() operation.Metadata { return OperationMetadata() }

func (definition *PlanningDefinition) Discover(ctx context.Context, input operation.DiscoverInput) (operation.Discovery, error) {
	pair, err := ParsePairParameters(input.Runtime.Parameters)
	if err != nil {
		return operation.Discovery{}, err
	}
	source, err := definition.discovery.Discover(ctx, pair.parametersFor(RoleSource))
	if err != nil {
		return operation.Discovery{}, fmt.Errorf("discover source ClickHouse: %w", err)
	}
	target, err := definition.discovery.Discover(ctx, pair.parametersFor(RoleTarget))
	if err != nil {
		return operation.Discovery{}, fmt.Errorf("discover target ClickHouse: %w", err)
	}
	pairSnapshot := PairSnapshot{Pair: pair, Source: source, Target: target}
	artifact, err := encodePairSnapshot(pairSnapshot)
	if err != nil {
		return operation.Discovery{}, err
	}
	targets, err := physicalTargets(pairSnapshot)
	if err != nil {
		return operation.Discovery{}, err
	}
	return operation.Discovery{Applicable: true, Summary: fmt.Sprintf("discovered ClickHouse %s -> %s for %d physical table(s)", pair.Source.Host, pair.Target.Host, len(targets)), Targets: targets, Snapshot: artifact}, nil
}

func (definition *PlanningDefinition) Precheck(ctx context.Context, input operation.PrecheckInput) (operation.Precheck, error) {
	snapshot, err := decodePairSnapshot(input.Discovery.Snapshot)
	if err != nil {
		return operation.Precheck{}, err
	}
	report := definition.prechecker.Check(snapshot.Source, snapshot.Target)
	findings := findingsFromCompatibility(report.Issues)

	if snapshot.Pair.StartTime != "" || snapshot.Pair.EndTime != "" {
		report.Compatible = false
		findings = append(findings, operation.Finding{Code: "TIME_RANGE_EXECUTION_NOT_READY", Severity: operation.FindingBlocking, Summary: "time-bounded migration is not enabled for the current atomic whole-table commit slice", Detail: "partition/range staging is implemented for transfer verification, but the bounded Apply path remains whole-table Atomic EXCHANGE only"})
	}
	for _, target := range physicalDataTables(snapshot.Target) {
		capability, capabilityErr := InspectCommitCapability(ctx, definition.client, snapshot.Pair.Target, target.Database, target.Name)
		if capabilityErr != nil {
			return operation.Precheck{}, capabilityErr
		}
		if !capability.ExchangeTables {
			report.Compatible = false
			findings = append(findings, operation.Finding{Code: "ATOMIC_EXCHANGE_NOT_AVAILABLE", Severity: operation.FindingBlocking, Summary: fmt.Sprintf("%s.%s is not executable by the current safe commit adapter", target.Database, target.Name), Detail: capability.Reason})
		}
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return operation.Precheck{}, err
	}
	summary := "ClickHouse precheck passed for the guarded Atomic EXCHANGE slice"
	if !report.Compatible {
		summary = "ClickHouse precheck blocked Apply"
	}
	return operation.Precheck{Passed: report.Compatible, Summary: summary, Snapshot: operation.Artifact{SchemaVersion: "clickhouse.precheck.v1", Payload: payload}, Findings: findings}, nil
}

func (definition *PlanningDefinition) Plan(_ context.Context, input operation.PlanInput) (operation.Plan, error) {
	if !input.Precheck.Passed {
		return operation.Plan{}, errors.New("cannot plan ClickHouse Apply while precheck is blocked")
	}
	snapshot, err := decodePairSnapshot(input.Discovery.Snapshot)
	if err != nil {
		return operation.Plan{}, err
	}
	var report PrecheckReport
	if err := json.Unmarshal(input.Precheck.Snapshot.Payload, &report); err != nil {
		return operation.Plan{}, fmt.Errorf("decode ClickHouse precheck: %w", err)
	}
	analysis := PairAnalysis{Pair: snapshot.Pair, Source: snapshot.Source, Target: snapshot.Target, Precheck: report, Plan: definition.planner.Build(snapshot.Pair.parametersFor(RoleSource), snapshot.Source, snapshot.Target, report)}
	// The final run-owned staging names are materialized from RestorePoint.RunID
	// immediately before Apply. The preparation ID is never used as a ledger owner.
	execution, err := BuildExchangeExecutionPlan("prepared", analysis)
	if err != nil {
		return operation.Plan{}, err
	}
	artifact, err := EncodeExchangeRestorePlan(execution)
	if err != nil {
		return operation.Plan{}, err
	}
	steps := make([]operation.PlanStep, 0, len(execution.Items)*3)
	for index, item := range execution.Items {
		target := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: item.Chunk.TargetDatabase + "." + item.Chunk.TargetTable}
		prefix := fmt.Sprintf("table-%d", index+1)
		steps = append(steps,
			operation.PlanStep{ID: prefix + "-stage", Name: "stage Native data", Target: target, Action: "native_stream_to_run_owned_staging", Checkpoint: "staged", Writes: true, RetrySafe: true, RollbackAction: "drop_run_owned_staging"},
			operation.PlanStep{ID: prefix + "-verify", Name: "verify staging fingerprint", Target: target, Action: "row_count_and_dual_hash", Checkpoint: "verified", Writes: false, RetrySafe: true},
			operation.PlanStep{ID: prefix + "-commit", Name: "atomic table exchange", Target: target, Action: "exchange_tables", Checkpoint: "committed", Writes: true, RetrySafe: false, RollbackAction: "guarded_reverse_exchange"},
		)
	}
	return operation.Plan{SchemaVersion: "clickhouse.operation_plan.v1", Summary: fmt.Sprintf("migrate %d ClickHouse physical table(s) through run-owned staging and guarded Atomic EXCHANGE", len(execution.Items)), Steps: steps, Execution: artifact}, nil
}

func (definition *PlanningDefinition) Impact(_ context.Context, input operation.ImpactInput) (operation.Impact, error) {
	execution, err := decodeExchangeRestorePlan(input.Plan.Execution)
	if err != nil {
		return operation.Impact{}, err
	}
	changes := make([]operation.Change, 0, len(execution.Items))
	for _, item := range execution.Items {
		changes = append(changes, operation.Change{Target: operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: item.Chunk.TargetDatabase + "." + item.Chunk.TargetTable}, Before: "verified target restore-point fingerprint", After: "source dataset fingerprint", Risk: "high"})
	}
	return operation.Impact{Summary: "creates a verified run-owned target restore point, stages ClickHouse data, then atomically exchanges target tables", Risk: operation.RiskHigh, Changes: changes, RequiresDowntime: false, RequiresWriteFence: true}, nil
}

func (definition *Definition) Apply(ctx context.Context, input operation.ApplyInput) (operation.ApplyResult, error) {
	manifest, err := definition.materializedManifest(input.RestorePoint)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	plan := manifest.Plan
	if input.Lease == nil {
		return operation.ApplyResult{}, errors.New("ClickHouse Apply requires an active operation lease")
	}
	executionEngine, err := NewNativeExecutionEngine(definition.ledger, definition.staging, definition.transport, definition.verifier)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	state := ApplyState{RunID: input.RestorePoint.RunID}
	for _, item := range plan.Items {
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return encodeApplyResult(state, errors.New("ClickHouse restore baseline is missing"))
		}
		if err := validateLeaseForItem(input.Lease, input.RestorePoint.RunID, item); err != nil {
			return encodeApplyResult(state, err)
		}
		sourceTable, ok := findTableByName(plan.Pair.Database, item.Chunk.SourceTable, plan, true)
		if !ok {
			return encodeApplyResult(state, fmt.Errorf("source table metadata missing for %s", item.Chunk.SourceTable))
		}
		if _, err := executionEngine.Execute(ctx, NativeChunkExecution{Pair: plan.Pair, Chunk: item.Chunk, SourceTable: sourceTable, TargetTable: item.TargetTable}); err != nil {
			return encodeApplyResult(state, err)
		}
		guard, err := guardForItem(input.Lease, input.RestorePoint.RunID, item)
		if err != nil {
			return encodeApplyResult(state, err)
		}
		commit, err := NewAtomicExchangeCommitEngine(definition.ledger, definition.client, definition.verifier, guard)
		if err != nil {
			return encodeApplyResult(state, err)
		}
		commitResult, err := commit.Commit(ctx, ExchangeCommitRequest{Pair: plan.Pair, Chunk: item.Chunk, TargetTable: item.TargetTable, TargetBaseline: &baseline.Fingerprint})
		if err != nil {
			return encodeApplyResult(state, err)
		}
		state.Committed = append(state.Committed, commitResult.Entry.Key)
	}
	return encodeApplyResult(state, nil)
}

func (definition *Definition) Verify(ctx context.Context, input operation.VerifyInput) (operation.Verification, error) {
	plan, err := definition.planFromApply(input.Plan, input.Apply)
	if err != nil {
		return operation.Verification{}, err
	}
	for _, item := range plan.Items {
		entry, ok, err := definition.ledger.Get(ctx, ledgerKeyForChunk(item.Chunk))
		if err != nil {
			return operation.Verification{}, err
		}
		if !ok || entry.State != LedgerCommitted {
			return operation.Verification{Passed: false, Summary: fmt.Sprintf("ClickHouse ledger is not committed for %s.%s", item.Chunk.TargetDatabase, item.Chunk.TargetTable)}, nil
		}
		fingerprint, err := definition.verifier.Fingerprint(ctx, plan.Pair.Target, item.Chunk.TargetDatabase, item.TargetTable, nil)
		if err != nil {
			return operation.Verification{}, err
		}
		if !CompareFingerprints(entry.Source, fingerprint).Passed {
			return operation.Verification{Passed: false, Summary: fmt.Sprintf("ClickHouse target fingerprint changed after commit for %s.%s", item.Chunk.TargetDatabase, item.Chunk.TargetTable)}, nil
		}
	}
	return operation.Verification{Passed: true, Summary: "all ClickHouse committed target tables match their verified source fingerprints"}, nil
}

func (definition *Definition) Rollback(ctx context.Context, input operation.RollbackInput) (operation.RollbackResult, error) {
	manifest, err := definition.materializedManifest(input.RestorePoint)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	plan := manifest.Plan
	if input.Lease == nil {
		return operation.RollbackResult{}, errors.New("ClickHouse rollback requires an active operation lease")
	}
	for index := len(plan.Items) - 1; index >= 0; index-- {
		item := plan.Items[index]
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return operation.RollbackResult{}, errors.New("ClickHouse restore baseline is missing")
		}
		if err := validateLeaseForItem(input.Lease, input.RestorePoint.RunID, item); err != nil {
			return operation.RollbackResult{}, err
		}
		entry, exists, err := definition.ledger.Get(ctx, ledgerKeyForChunk(item.Chunk))
		if err != nil {
			return operation.RollbackResult{}, err
		}
		if !exists {
			continue
		}
		switch entry.State {
		case LedgerCommitted, LedgerCommitUnknown:
			guard, err := guardForItem(input.Lease, input.RestorePoint.RunID, item)
			if err != nil {
				return operation.RollbackResult{}, err
			}
			commit, err := NewAtomicExchangeCommitEngine(definition.ledger, definition.client, definition.verifier, guard)
			if err != nil {
				return operation.RollbackResult{}, err
			}
			if _, err := commit.Rollback(ctx, ExchangeCommitRequest{Pair: plan.Pair, Chunk: item.Chunk, TargetTable: item.TargetTable, TargetBaseline: &baseline.Fingerprint}); err != nil {
				return operation.RollbackResult{}, err
			}
		case LedgerRollbackPending:
			if entry.Checkpoint == "staging_drop_pending" {
				if err := definition.rollbackPrecommitStaging(ctx, entry, plan.Pair.Target, item.Chunk); err != nil {
					return operation.RollbackResult{}, err
				}
				continue
			}
			guard, err := guardForItem(input.Lease, input.RestorePoint.RunID, item)
			if err != nil {
				return operation.RollbackResult{}, err
			}
			commit, err := NewAtomicExchangeCommitEngine(definition.ledger, definition.client, definition.verifier, guard)
			if err != nil {
				return operation.RollbackResult{}, err
			}
			if _, err := commit.ReconcileRollback(ctx, ExchangeCommitRequest{Pair: plan.Pair, Chunk: item.Chunk, TargetTable: item.TargetTable, TargetBaseline: &baseline.Fingerprint}); err != nil {
				return operation.RollbackResult{}, err
			}
		case LedgerRolledBack:
			continue
		default:
			if err := definition.rollbackPrecommitStaging(ctx, entry, plan.Pair.Target, item.Chunk); err != nil {
				return operation.RollbackResult{}, err
			}
		}
	}
	payload, _ := json.Marshal(map[string]string{"run_id": input.RestorePoint.RunID, "status": "restored"})
	return operation.RollbackResult{Restored: true, Checkpoint: "clickhouse_rollback_verified", State: operation.Artifact{SchemaVersion: "clickhouse.rollback.v1", Payload: payload}}, nil
}

func (definition *Definition) VerifyRollback(ctx context.Context, input operation.VerifyRollbackInput) (operation.Verification, error) {
	if !input.Rollback.Restored {
		return operation.Verification{Passed: false, Summary: "ClickHouse rollback result is not restored"}, nil
	}
	manifest, err := definition.materializedManifest(input.RestorePoint)
	if err != nil {
		return operation.Verification{}, err
	}
	plan := manifest.Plan
	for _, item := range plan.Items {
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return operation.Verification{}, errors.New("ClickHouse restore baseline is missing")
		}
		fingerprint, err := definition.verifier.Fingerprint(ctx, plan.Pair.Target, item.Chunk.TargetDatabase, item.TargetTable, nil)
		if err != nil {
			return operation.Verification{}, err
		}
		if !matchesTargetBaseline(baseline.Fingerprint, fingerprint) {
			return operation.Verification{Passed: false, Summary: fmt.Sprintf("rollback baseline does not match the restore point for %s.%s", item.Chunk.TargetDatabase, item.Chunk.TargetTable)}, nil
		}
	}
	return operation.Verification{Passed: true, Summary: "ClickHouse rollback restored all verified target baselines"}, nil
}

func encodePairSnapshot(snapshot PairSnapshot) (operation.Artifact, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return operation.Artifact{}, err
	}
	return operation.Artifact{SchemaVersion: pairSnapshotSchema, Payload: payload}, nil
}

func decodePairSnapshot(artifact operation.Artifact) (PairSnapshot, error) {
	if artifact.SchemaVersion != pairSnapshotSchema {
		return PairSnapshot{}, fmt.Errorf("unsupported ClickHouse pair snapshot %q", artifact.SchemaVersion)
	}
	var snapshot PairSnapshot
	if err := json.Unmarshal(artifact.Payload, &snapshot); err != nil {
		return PairSnapshot{}, err
	}
	return snapshot, nil
}

func physicalTargets(snapshot PairSnapshot) ([]operation.Target, error) {
	seen := map[string]struct{}{}
	var targets []operation.Target
	for _, table := range physicalDataTables(snapshot.Target) {
		resource := table.Database + "." + table.Name
		if _, ok := seen[resource]; ok {
			continue
		}
		seen[resource] = struct{}{}
		target := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: resource}
		if err := operation.ValidateTarget(target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Resource < targets[j].Resource })
	return targets, nil
}

func physicalDataTables(snapshot Snapshot) []Table {
	seen := map[string]struct{}{}
	var tables []Table
	for _, logicalName := range snapshot.Requested {
		logical, ok := findTable(snapshot, snapshot.Database, logicalName)
		if !ok {
			continue
		}
		physical, ok := resolveDataTable(snapshot, logical)
		if !ok {
			continue
		}
		key := physical.Database + "." + physical.Name
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		tables = append(tables, physical)
	}
	return tables
}

func findingsFromCompatibility(issues []CompatibilityIssue) []operation.Finding {
	findings := make([]operation.Finding, 0, len(issues))
	for _, issue := range issues {
		severity := operation.FindingWarning
		if strings.EqualFold(issue.Severity, "blocking") || strings.EqualFold(issue.Severity, "error") {
			severity = operation.FindingBlocking
		}
		findings = append(findings, operation.Finding{Code: issue.Code, Severity: severity, Summary: issue.Summary, Detail: issue.Detail})
	}
	return findings
}

func (definition *Definition) materializedPlan(point operation.RestorePoint) (ExchangeRestorePlan, error) {
	manifest, err := definition.materializedManifest(point)
	if err != nil {
		return ExchangeRestorePlan{}, err
	}
	return manifest.Plan, nil
}

func (definition *Definition) materializedManifest(point operation.RestorePoint) (ExchangeRestoreManifest, error) {
	if point.RunID == "" {
		return ExchangeRestoreManifest{}, errors.New("ClickHouse restore point run ID is required")
	}
	var manifest ExchangeRestoreManifest
	if point.Manifest.SchemaVersion != ExchangeRestoreManifestSchema {
		return ExchangeRestoreManifest{}, fmt.Errorf("unsupported ClickHouse restore manifest %q", point.Manifest.SchemaVersion)
	}
	if err := json.Unmarshal(point.Manifest.Payload, &manifest); err != nil {
		return ExchangeRestoreManifest{}, err
	}
	for index := range manifest.Plan.Items {
		manifest.Plan.Items[index].Chunk.RunID = point.RunID
		staging, err := BuildStagingTableName(point.RunID, manifest.Plan.Items[index].Chunk.TargetTable)
		if err != nil {
			return ExchangeRestoreManifest{}, err
		}
		manifest.Plan.Items[index].Chunk.StagingTable = staging
	}
	return manifest, nil
}

func (definition *Definition) planFromApply(planArtifact operation.Plan, apply operation.ApplyResult) (ExchangeRestorePlan, error) {
	var state ApplyState
	if apply.State.SchemaVersion != applyStateSchema {
		return ExchangeRestorePlan{}, fmt.Errorf("unsupported ClickHouse apply state %q", apply.State.SchemaVersion)
	}
	if err := json.Unmarshal(apply.State.Payload, &state); err != nil {
		return ExchangeRestorePlan{}, err
	}
	template, err := decodeExchangeRestorePlan(planArtifact.Execution)
	if err != nil {
		return ExchangeRestorePlan{}, err
	}
	for index := range template.Items {
		template.Items[index].Chunk.RunID = state.RunID
		staging, err := BuildStagingTableName(state.RunID, template.Items[index].Chunk.TargetTable)
		if err != nil {
			return ExchangeRestorePlan{}, err
		}
		template.Items[index].Chunk.StagingTable = staging
	}
	return template, nil
}

func encodeApplyResult(state ApplyState, cause error) (operation.ApplyResult, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	result := operation.ApplyResult{Changed: len(state.Committed) > 0, Checkpoint: "clickhouse_apply", State: operation.Artifact{SchemaVersion: applyStateSchema, Payload: payload}}
	return result, cause
}

func validateLeaseForItem(lease operation.LeaseHandle, runID string, item ExchangeRestoreItem) error {
	if err := lease.Validate(time.Now().UTC()); err != nil {
		return err
	}
	target := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: item.Chunk.TargetDatabase + "." + item.Chunk.TargetTable}
	key, err := operation.ResourceLockKey(target)
	if err != nil {
		return err
	}
	return operation.ValidateLeaseCoverage(lease.Current(), runID, []operation.LockResource{{Key: key}}, time.Now().UTC())
}

func guardForItem(lease operation.LeaseHandle, runID string, item ExchangeRestoreItem) (*LeaseCommitGuard, error) {
	if err := validateLeaseForItem(lease, runID, item); err != nil {
		return nil, err
	}
	target := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: item.Chunk.TargetDatabase + "." + item.Chunk.TargetTable}
	key, err := operation.ResourceLockKey(target)
	if err != nil {
		return nil, err
	}
	return NewLeaseCommitGuard(lease.Current(), key)
}

func (definition *Definition) rollbackPrecommitStaging(ctx context.Context, entry LedgerEntry, endpoint Endpoint, chunk TransferChunk) error {
	cause := errors.New("pre-commit rollback requested")
	result, err := reconcileNativeStagingCleanup(ctx, definition.ledger, definition.staging, time.Now, entry, endpoint, chunk, cause)
	if result.Entry.State == LedgerRolledBack && errors.Is(err, cause) {
		return nil
	}
	return err
}

// The execution manifest stores target table metadata. Source metadata is
// reconstructed from the selected target columns because precheck already proved
// the source and target schemas equivalent for the current native-stream slice.
func findTableByName(database, name string, plan ExchangeRestorePlan, source bool) (Table, bool) {
	for _, item := range plan.Items {
		candidate := item.TargetTable
		if source {
			candidate.Name = item.Chunk.SourceTable
			candidate.Database = item.Chunk.SourceDatabase
		}
		if candidate.Database == database && candidate.Name == name {
			return candidate, true
		}
	}
	return Table{}, false
}

var _ operation.OperationDefinition = (*Definition)(nil)
var _ operation.PlanningDefinition = (*PlanningDefinition)(nil)
