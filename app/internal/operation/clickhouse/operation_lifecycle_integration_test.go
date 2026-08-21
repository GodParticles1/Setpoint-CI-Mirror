package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"setpoint/internal/operation"
)

type lifecycleDataState struct {
	mu     sync.Mutex
	source DataFingerprint
	tables map[string]DataFingerprint
}

func newLifecycleDataState(source DataFingerprint) *lifecycleDataState {
	return &lifecycleDataState{source: source, tables: map[string]DataFingerprint{"events": {Rows: 0}}}
}

func (state *lifecycleDataState) get(endpoint Endpoint, table string) DataFingerprint {
	state.mu.Lock()
	defer state.mu.Unlock()
	if endpoint.Host == "source" {
		return state.source
	}
	return state.tables[table]
}
func (state *lifecycleDataState) exists(table string) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	_, ok := state.tables[table]
	return ok
}
func (state *lifecycleDataState) set(table string, value DataFingerprint) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.tables[table] = value
}
func (state *lifecycleDataState) drop(table string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	delete(state.tables, table)
}
func (state *lifecycleDataState) exchange(left, right string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.tables[left], state.tables[right] = state.tables[right], state.tables[left]
}

type lifecycleFingerprintVerifier struct{ state *lifecycleDataState }

func (verifier lifecycleFingerprintVerifier) Fingerprint(_ context.Context, endpoint Endpoint, _ string, table Table, _ *TimeRangeFilter) (DataFingerprint, error) {
	return verifier.state.get(endpoint, table.Name), nil
}

type lifecycleStagingController struct{ state *lifecycleDataState }

func (controller lifecycleStagingController) Recreate(_ context.Context, _ Endpoint, _ string, staging, _ string) error {
	controller.state.set(staging, DataFingerprint{})
	return nil
}
func (controller lifecycleStagingController) Drop(_ context.Context, _ Endpoint, _ string, staging string) error {
	controller.state.drop(staging)
	return nil
}
func (controller lifecycleStagingController) Exists(_ context.Context, _ Endpoint, _ string, staging string) (bool, error) {
	return controller.state.exists(staging), nil
}

type lifecycleRestoreObjects struct {
	state *lifecycleDataState
	base  *memoryRestoreObjects
}

func (objects lifecycleRestoreObjects) Inspect(ctx context.Context, endpoint Endpoint, database, table string) (RestoreObjectSnapshot, error) {
	return objects.base.Inspect(ctx, endpoint, database, table)
}
func (objects lifecycleRestoreObjects) Create(ctx context.Context, endpoint Endpoint, database, restoreTable string, target Table) error {
	if err := objects.base.Create(ctx, endpoint, database, restoreTable, target); err != nil {
		return err
	}
	objects.state.set(restoreTable, objects.state.get(endpoint, target.Name))
	return nil
}
func (objects lifecycleRestoreObjects) Drop(ctx context.Context, endpoint Endpoint, database, table string) error {
	if err := objects.base.Drop(ctx, endpoint, database, table); err != nil {
		return err
	}
	objects.state.drop(table)
	return nil
}

type lifecycleNativeTransport struct {
	state     *lifecycleDataState
	failTable string
}

func (transport lifecycleNativeTransport) Transfer(_ context.Context, request NativeTransferRequest) (NativeTransferResult, error) {
	if request.Chunk.TargetTable == transport.failTable {
		return NativeTransferResult{}, errors.New("injected transport interruption")
	}
	transport.state.set(request.Chunk.StagingTable, transport.state.source)
	return NativeTransferResult{BytesTransferred: 128, SourceExitCode: 0, TargetExitCode: 0}, nil
}

type lifecycleQueryClient struct{ state *lifecycleDataState }

func (client lifecycleQueryClient) Query(_ context.Context, request QueryRequest) (string, error) {
	switch {
	case strings.Contains(request.Query, "FROM system.databases"):
		return "Atomic", nil
	case strings.Contains(request.Query, "FROM system.tables"):
		return "MergeTree", nil
	case strings.HasPrefix(request.Query, "EXPLAIN SYNTAX EXCHANGE TABLES"):
		return strings.TrimPrefix(request.Query, "EXPLAIN SYNTAX "), nil
	case strings.HasPrefix(request.Query, "EXCHANGE TABLES"):
		parts := strings.Fields(request.Query)
		if len(parts) != 5 || parts[3] != "AND" {
			return "", fmt.Errorf("unexpected EXCHANGE query %q", request.Query)
		}
		left := strings.Trim(parts[2], "`")
		right := strings.Trim(parts[4], "`")
		left = strings.TrimPrefix(left, "db`.`")
		right = strings.TrimPrefix(right, "db`.`")
		client.state.exchange(left, right)
		return "", nil
	default:
		return "", nil
	}
}

type lifecycleOperationLocks struct {
	mu       sync.Mutex
	released int
}

func (locks *lifecycleOperationLocks) Acquire(_ context.Context, request operation.LockRequest) (operation.LockLease, error) {
	now := time.Now().UTC()
	return operation.LockLease{ID: "lease-ch-1", OwnerID: request.OwnerID, Resources: append([]operation.LockResource(nil), request.Resources...), AcquiredAt: now, ExpiresAt: now.Add(request.TTL)}, nil
}
func (locks *lifecycleOperationLocks) Renew(_ context.Context, lease operation.LockLease, ttl time.Duration) (operation.LockLease, error) {
	lease.ExpiresAt = time.Now().UTC().Add(ttl)
	return lease, nil
}
func (locks *lifecycleOperationLocks) Release(context.Context, operation.LockLease) error {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	locks.released++
	return nil
}

type lifecycleOperationJournal struct {
	mu      sync.Mutex
	entries []operation.JournalEntry
}

func (journal *lifecycleOperationJournal) Append(_ context.Context, entry operation.JournalEntry) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.entries = append(journal.entries, entry)
	return nil
}
func (journal *lifecycleOperationJournal) List(_ context.Context, runID string) ([]operation.JournalEntry, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	var result []operation.JournalEntry
	for _, entry := range journal.entries {
		if entry.RunID == runID {
			result = append(result, entry)
		}
	}
	return result, nil
}

type lifecycleAllowCommitGuard struct{}

func (lifecycleAllowCommitGuard) Verify(context.Context, CommitGuardRequest) error { return nil }

func TestClickHouseDefinitionRunsThroughControlledOperationCoordinator(t *testing.T) {
	sourceFingerprint := DataFingerprint{Rows: 10, Bytes: 100, HashSum64: "1000", HashXor64: "99"}
	targetBaseline := DataFingerprint{Rows: 4, Bytes: 40, HashSum64: "400", HashXor64: "44"}
	state := newLifecycleDataState(sourceFingerprint)
	state.set("events", targetBaseline)
	client := lifecycleQueryClient{state: state}
	verifier := lifecycleFingerprintVerifier{state: state}
	staging := lifecycleStagingController{state: state}
	transport := lifecycleNativeTransport{state: state}
	ledger := newMemoryLedger()
	definition, err := NewDefinition(client, ledger, staging, transport, verifier)
	if err != nil {
		t.Fatal(err)
	}

	commitForRestore, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, lifecycleAllowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	targetTable := Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Position: 1, Type: "UInt64"}}}
	store := newMemoryRestoreStore()
	objects := lifecycleRestoreObjects{state: state, base: newMemoryRestoreObjects(t, targetTable)}
	restore, err := NewExchangeRestoreProvider(ledger, store, staging, objects, verifier, commitForRestore)
	if err != nil {
		t.Fatal(err)
	}
	locks := &lifecycleOperationLocks{}
	journal := &lifecycleOperationJournal{}
	coordinator, err := operation.NewCoordinator(locks, journal, restore)
	if err != nil {
		t.Fatal(err)
	}

	pair := PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}
	chunk := TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_prepared", Sequence: 1}
	executionPlan := ExchangeRestorePlan{Pair: pair, Items: []ExchangeRestoreItem{{Chunk: chunk, TargetTable: targetTable}}}
	executionArtifact, err := EncodeExchangeRestorePlan(executionPlan)
	if err != nil {
		t.Fatal(err)
	}

	target := operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}
	prepared := operation.PreparedRun{
		RunID:     "run-lifecycle-1",
		Metadata:  definition.Metadata(),
		Runtime:   operation.RuntimeInput{System: "linux"},
		Discovery: operation.Discovery{Applicable: true, Targets: []operation.Target{target}, Snapshot: operation.Artifact{SchemaVersion: pairSnapshotSchema, Payload: json.RawMessage(`{}`)}},
		Precheck:  operation.Precheck{Passed: true},
		Plan:      operation.Plan{SchemaVersion: "clickhouse.operation_plan.v1", Execution: executionArtifact},
		Impact:    operation.Impact{Risk: operation.RiskHigh, RequiresWriteFence: true},
		State:     operation.StateAwaitingConfirm,
	}

	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != operation.StateSucceeded {
		t.Fatalf("state=%s", result.State)
	}
	if !result.Verification.Passed {
		t.Fatalf("verification=%#v", result.Verification)
	}
	if got := state.get(pair.Target, "events"); !CompareFingerprints(sourceFingerprint, got).Passed {
		t.Fatalf("target fingerprint=%#v", got)
	}
	records, err := store.ListRestores(context.Background(), prepared.RunID)
	if err != nil || len(records) != 1 || records[0].State != RestoreReady || records[0].Baseline != targetBaseline {
		t.Fatalf("restore records=%#v err=%v", records, err)
	}
	if locks.released != 1 {
		t.Fatalf("lock releases=%d", locks.released)
	}
	entries, err := ledger.ListRun(context.Background(), prepared.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].State != LedgerCommitted {
		t.Fatalf("ledger=%#v", entries)
	}
	if len(journal.entries) == 0 || journal.entries[len(journal.entries)-1].State != operation.StateSucceeded {
		t.Fatalf("journal tail=%#v", journal.entries)
	}
}

func TestClickHouseCoordinatorRestoresEarlierTableWhenLaterApplyStops(t *testing.T) {
	sourceFingerprint := DataFingerprint{Rows: 10, Bytes: 100, HashSum64: "1000", HashXor64: "99"}
	state := newLifecycleDataState(sourceFingerprint)
	state.set("metrics", DataFingerprint{})
	client := lifecycleQueryClient{state: state}
	verifier := lifecycleFingerprintVerifier{state: state}
	staging := lifecycleStagingController{state: state}
	transport := lifecycleNativeTransport{state: state, failTable: "metrics"}
	ledger := newMemoryLedger()
	definition, err := NewDefinition(client, ledger, staging, transport, verifier)
	if err != nil {
		t.Fatal(err)
	}

	commitForRestore, err := NewAtomicExchangeCommitEngine(ledger, client, verifier, lifecycleAllowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	columns := []Column{{Name: "id", Position: 1, Type: "UInt64"}}
	targetTables := []Table{
		{Database: "db", Name: "events", Engine: "MergeTree", Columns: columns},
		{Database: "db", Name: "metrics", Engine: "MergeTree", Columns: columns},
	}
	store := newMemoryRestoreStore()
	objects := lifecycleRestoreObjects{state: state, base: newMemoryRestoreObjects(t, targetTables...)}
	restore, err := NewExchangeRestoreProvider(ledger, store, staging, objects, verifier, commitForRestore)
	if err != nil {
		t.Fatal(err)
	}
	locks := &lifecycleOperationLocks{}
	journal := &lifecycleOperationJournal{}
	coordinator, err := operation.NewCoordinator(locks, journal, restore)
	if err != nil {
		t.Fatal(err)
	}

	pair := PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events", "metrics"}}
	items := []ExchangeRestoreItem{
		{Chunk: TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_prepared", Sequence: 1}, TargetTable: Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: columns}},
		{Chunk: TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "metrics", TargetDatabase: "db", TargetTable: "metrics", StagingTable: "spmig_metrics_prepared", Sequence: 2}, TargetTable: Table{Database: "db", Name: "metrics", Engine: "MergeTree", Columns: columns}},
	}
	executionArtifact, err := EncodeExchangeRestorePlan(ExchangeRestorePlan{Pair: pair, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	prepared := operation.PreparedRun{
		RunID:    "run-lifecycle-partial",
		Metadata: definition.Metadata(),
		Runtime:  operation.RuntimeInput{System: "linux"},
		Discovery: operation.Discovery{Applicable: true, Targets: []operation.Target{
			{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"},
			{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.metrics"},
		}},
		Precheck: operation.Precheck{Passed: true},
		Plan:     operation.Plan{SchemaVersion: "clickhouse.operation_plan.v1", Execution: executionArtifact},
		Impact:   operation.Impact{Risk: operation.RiskHigh, RequiresWriteFence: true},
		State:    operation.StateAwaitingConfirm,
	}

	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err == nil || !strings.Contains(err.Error(), "injected transport interruption") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.State != operation.StateRolledBack {
		t.Fatalf("state=%s err=%v result=%#v", result.State, err, result)
	}
	if !result.Rollback.Restored || !result.RollbackVerification.Passed {
		t.Fatalf("rollback=%#v verification=%#v", result.Rollback, result.RollbackVerification)
	}
	for _, table := range []string{"events", "metrics"} {
		if got := state.get(pair.Target, table); got.Rows != 0 {
			t.Fatalf("target %s fingerprint=%#v", table, got)
		}
	}
	records, restoreErr := store.ListRestores(context.Background(), prepared.RunID)
	if restoreErr != nil || len(records) != 2 {
		t.Fatalf("restore records=%#v err=%v", records, restoreErr)
	}
	for _, record := range records {
		if record.State != RestoreCleaned {
			t.Fatalf("restore record not cleaned: %#v", record)
		}
	}
	entries, listErr := ledger.ListRun(context.Background(), prepared.RunID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 2 {
		t.Fatalf("ledger=%#v", entries)
	}
	for _, entry := range entries {
		if entry.State != LedgerRolledBack {
			t.Fatalf("ledger entry=%#v", entry)
		}
	}
	if locks.released != 1 {
		t.Fatalf("lock releases=%d", locks.released)
	}
	if len(journal.entries) == 0 || journal.entries[len(journal.entries)-1].State != operation.StateRolledBack {
		t.Fatalf("journal tail=%#v", journal.entries)
	}
}
