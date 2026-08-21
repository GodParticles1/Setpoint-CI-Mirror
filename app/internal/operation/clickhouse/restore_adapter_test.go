package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"setpoint/internal/operation"
)

var testRestoreOwnershipToken = strings.Repeat("01", restoreOwnershipTokenBytes)

type noOpStaging struct {
	drops  int
	exists bool
}

func (stage *noOpStaging) Recreate(context.Context, Endpoint, string, string, string) error {
	stage.exists = true
	return nil
}
func (stage *noOpStaging) Drop(context.Context, Endpoint, string, string) error {
	stage.drops++
	stage.exists = false
	return nil
}
func (stage *noOpStaging) Exists(context.Context, Endpoint, string, string) (bool, error) {
	return stage.exists, nil
}

type restoreVerifier struct{ fingerprint DataFingerprint }

func (verifier *restoreVerifier) Fingerprint(context.Context, Endpoint, string, Table, *TimeRangeFilter) (DataFingerprint, error) {
	return verifier.fingerprint, nil
}

func TestEmptyTargetRestorePointCreateAndVerify(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &noOpStaging{}
	query := &commitQueryClient{}
	verifier := &restoreVerifier{fingerprint: DataFingerprint{Rows: 0}}
	commit, err := NewAtomicExchangeCommitEngine(ledger, query, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	targetTable := Table{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}}
	provider, _, _ := newTestRestoreProvider(t, ledger, stage, verifier, commit, targetTable)
	plan := ExchangeRestorePlan{Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}, Items: []ExchangeRestoreItem{{Chunk: TransferChunk{RunID: "run-1", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: "spmig_events_123", Sequence: 1}, TargetTable: targetTable}}}
	artifact, err := EncodeExchangeRestorePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	point, err := provider.Create(context.Background(), operation.RestorePointRequest{OperationID: OperationID, RunID: "run-1", Targets: []operation.Target{{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}}, Plan: operation.Plan{Execution: artifact}, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if point.Status != operation.RestorePointVerified || point.ProviderID != ExchangeRestoreProviderID {
		t.Fatalf("point=%#v", point)
	}
	verification, err := provider.Verify(context.Background(), point)
	if err != nil || !verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
}

type nonEmptyRestoreFixture struct {
	ledger   *memoryLedger
	store    *memoryRestoreStore
	objects  *memoryRestoreObjects
	stage    *noOpStaging
	provider *ExchangeRestoreProvider
	pair     PairParameters
	request  operation.RestorePointRequest
	target   Table
	baseline DataFingerprint
}

func newNonEmptyRestoreFixture(t *testing.T, runID string) *nonEmptyRestoreFixture {
	t.Helper()
	target := Table{
		Database: "db", Name: "events", Engine: "MergeTree", EngineFull: "MergeTree()", SortingKey: "id", PrimaryKey: "id",
		Columns:    []Column{{Name: "id", Position: 1, Type: "UInt64"}},
		Partitions: []Partition{{Partition: "all", Rows: 4, BytesOnDisk: 64, ActiveParts: 1}},
	}
	baseline := DataFingerprint{Rows: 4, Bytes: 64, HashSum64: "40", HashXor64: "4"}
	ledger := newMemoryLedger()
	store := newMemoryRestoreStore()
	objects := newMemoryRestoreObjects(t, target)
	objects.setFingerprint("db", "events", baseline)
	stage := &noOpStaging{}
	commit, err := NewAtomicExchangeCommitEngine(ledger, &commitQueryClient{}, objects, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewExchangeRestoreProvider(ledger, store, stage, objects, objects, commit)
	if err != nil {
		t.Fatal(err)
	}
	provider.newToken = func() (string, error) { return testRestoreOwnershipToken, nil }
	preparedStaging, err := BuildStagingTableName("prepared", "events")
	if err != nil {
		t.Fatal(err)
	}
	pair := PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events"}}
	plan := ExchangeRestorePlan{
		Pair: pair,
		Items: []ExchangeRestoreItem{{
			Chunk:       TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: "events", TargetDatabase: "db", TargetTable: "events", StagingTable: preparedStaging, Sequence: 1},
			TargetTable: target,
		}},
	}
	artifact, err := EncodeExchangeRestorePlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	request := operation.RestorePointRequest{
		OperationID: OperationID,
		RunID:       runID,
		Targets:     []operation.Target{{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db.events"}},
		Plan:        operation.Plan{Execution: artifact},
		Retention:   time.Hour,
	}
	return &nonEmptyRestoreFixture{ledger: ledger, store: store, objects: objects, stage: stage, provider: provider, pair: pair, request: request, target: target, baseline: baseline}
}

func (fixture *nonEmptyRestoreFixture) create(t *testing.T) operation.RestorePoint {
	t.Helper()
	point, err := fixture.provider.Create(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	return point
}

func TestNonEmptyRestorePointPersistsIntentCreatesAndVerifiesCopy(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-nonempty")
	point := fixture.create(t)

	record, ok, err := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: "run-nonempty", Database: "db", Table: "events"})
	if err != nil || !ok {
		t.Fatalf("restore record missing: ok=%v err=%v", ok, err)
	}
	if record.State != RestoreReady || record.Baseline != fixture.baseline || record.Restore.UUID == "" || fixture.objects.creates != 1 {
		t.Fatalf("record=%#v creates=%d", record, fixture.objects.creates)
	}
	verification, err := fixture.provider.Verify(context.Background(), point)
	if err != nil || !verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
}

func TestRestoreCreationReconcilesAfterReadyPersistenceFailureWithoutReplay(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-ready-persist")
	fixture.store.failAt = 4
	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "persist verified") {
		t.Fatalf("expected ready persistence failure, got %v", err)
	}
	record, ok, _ := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if !ok || record.State != RestoreCreating || record.Restore.UUID == "" || fixture.objects.creates != 1 {
		t.Fatalf("record=%#v creates=%d", record, fixture.objects.creates)
	}
	point := fixture.create(t)
	if fixture.objects.creates != 1 {
		t.Fatalf("restore mutation replayed: creates=%d", fixture.objects.creates)
	}
	if verification, err := fixture.provider.Verify(context.Background(), point); err != nil || !verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
}

func TestRestoreCreationReconcilesGeneratedIdentityPersistenceFailureWithoutReplay(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-identity-persist")
	fixture.store.failAt = 3
	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "persist generated") {
		t.Fatalf("expected generated identity persistence failure, got %v", err)
	}
	record, ok, _ := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if !ok || record.State != RestoreCreating || record.Restore.UUID != "" || fixture.objects.creates != 1 {
		t.Fatalf("record=%#v creates=%d", record, fixture.objects.creates)
	}
	point := fixture.create(t)
	if fixture.objects.creates != 1 {
		t.Fatalf("restore mutation replayed: creates=%d", fixture.objects.creates)
	}
	if verification, err := fixture.provider.Verify(context.Background(), point); err != nil || !verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
}

func TestRestoreCreationPreservesUnexpectedObjectWhileCreating(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-foreign-restart")
	fixture.store.failAt = 4
	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "persist verified") {
		t.Fatalf("expected ready persistence failure, got %v", err)
	}
	record, ok, err := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if err != nil || !ok || record.State != RestoreCreating {
		t.Fatalf("record=%#v ok=%v err=%v", record, ok, err)
	}
	key := record.Restore.Database + "." + record.Restore.Table
	fixture.objects.mu.Lock()
	snapshot := fixture.objects.objects[key]
	snapshot.Identity.UUID = "foreign-uuid"
	fixture.objects.objects[key] = snapshot
	fixture.objects.mu.Unlock()

	_, createErr := fixture.provider.Create(context.Background(), fixture.request)
	if createErr == nil || !strings.Contains(createErr.Error(), "cannot be proven owned") {
		t.Fatalf("unexpected object accepted: %v", createErr)
	}
	record, ok, err = fixture.store.GetRestore(context.Background(), record.Key)
	if err != nil || !ok || record.State != RestoreManualReview || fixture.objects.drops != 0 || fixture.objects.creates != 1 {
		t.Fatalf("record=%#v ok=%v err=%v create_err=%v creates=%d drops=%d", record, ok, err, createErr, fixture.objects.creates, fixture.objects.drops)
	}
	remaining, err := fixture.objects.Inspect(context.Background(), fixture.pair.Target, record.Restore.Database, record.Restore.Table)
	if err != nil || !remaining.Exists {
		t.Fatalf("unexpected object was removed: snapshot=%#v err=%v", remaining, err)
	}
}

func TestRestoreCreationPreservesPartialOwnedObjectForManualReview(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-partial-create")
	fixture.objects.createErr = errors.New("injected copy failure")
	fixture.objects.partialCreate = true
	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "copy failure") {
		t.Fatalf("expected partial create failure, got %v", err)
	}
	_, createErr := fixture.provider.Create(context.Background(), fixture.request)
	if createErr == nil || !strings.Contains(createErr.Error(), "cannot be proven owned") {
		t.Fatalf("partial object unexpectedly accepted: %v", createErr)
	}
	record, ok, err := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if err != nil || !ok || record.State != RestoreManualReview || fixture.objects.creates != 1 || fixture.objects.drops != 0 {
		t.Fatalf("record=%#v ok=%v err=%v creates=%d drops=%d", record, ok, err, fixture.objects.creates, fixture.objects.drops)
	}
	remaining, err := fixture.objects.Inspect(context.Background(), fixture.pair.Target, record.Restore.Database, record.Restore.Table)
	if err != nil || !remaining.Exists {
		t.Fatalf("partial object was removed: snapshot=%#v err=%v", remaining, err)
	}
}

func TestRestoreIntentRejectsTargetWithoutUUID(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-target-uuid-missing")
	fixture.objects.mu.Lock()
	target := fixture.objects.objects["db.events"]
	target.Identity.UUID = ""
	fixture.objects.objects["db.events"] = target
	fixture.objects.mu.Unlock()

	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "target UUID") {
		t.Fatalf("target without UUID accepted: %v", err)
	}
	if fixture.objects.creates != 0 {
		t.Fatalf("restore mutation occurred before target ownership was proven: creates=%d", fixture.objects.creates)
	}
}

func TestRestoreOwnershipTokenFailureStopsBeforeIntentAndMutation(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-token-failure")
	fixture.provider.newToken = func() (string, error) { return "", errors.New("entropy unavailable") }

	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("ownership token failure=%v", err)
	}
	if fixture.objects.creates != 0 || len(fixture.store.records) != 0 {
		t.Fatalf("state changed without ownership token: creates=%d records=%d", fixture.objects.creates, len(fixture.store.records))
	}
}

func TestRestoreIntentRejectsPreexistingDeterministicObject(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-name-occupied")
	restoreTable, err := BuildRestoreTableName(fixture.request.RunID, fixture.target.Name, testRestoreOwnershipToken)
	if err != nil {
		t.Fatal(err)
	}
	fixture.objects.objects["db."+restoreTable] = RestoreObjectSnapshot{Exists: true, Identity: RestoreObjectIdentity{Database: "db", Table: restoreTable, UUID: "foreign", Engine: fixture.target.Engine, SchemaFingerprint: fixture.objects.objects["db.events"].Identity.SchemaFingerprint}}
	fixture.objects.fingerprints["db."+restoreTable] = fixture.baseline
	if _, err := fixture.provider.Create(context.Background(), fixture.request); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("preexisting object accepted: %v", err)
	}
	if fixture.objects.creates != 0 {
		t.Fatalf("unexpected restore mutation: creates=%d", fixture.objects.creates)
	}
	record, ok, recordErr := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if recordErr != nil || !ok || record.State != RestoreManualReview || fixture.objects.drops != 0 {
		t.Fatalf("record=%#v ok=%v err=%v drops=%d", record, ok, recordErr, fixture.objects.drops)
	}
}

func TestRestoreVerificationBlocksTargetDriftAndPreservesRestoreObject(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-target-drift")
	point := fixture.create(t)
	fixture.objects.setFingerprint("db", "events", DataFingerprint{Rows: 5, HashSum64: "50", HashXor64: "5"})
	verification, err := fixture.provider.Verify(context.Background(), point)
	if err != nil || verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
	manifest, err := fixture.provider.decodeManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := fixture.objects.Inspect(context.Background(), manifest.Plan.Pair.Target, "db", manifest.Baselines[0].Restore.Table)
	if err != nil || !backup.Exists {
		t.Fatalf("restore object not preserved: %#v err=%v", backup, err)
	}
}

func TestVerifiedRollbackRestoresOriginalFingerprintThenCleansOwnedObjects(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-cleanup")
	point := fixture.create(t)
	manifest, err := fixture.provider.decodeManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	item := manifest.Plan.Items[0]
	fixture.stage.exists = true
	if err := fixture.ledger.Put(context.Background(), LedgerEntry{Key: ledgerKeyForChunk(item.Chunk), Strategy: item.Chunk.Strategy, State: LedgerRolledBack, Attempt: 1, StagingTable: item.Chunk.StagingTable, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	verification, err := fixture.provider.VerifyRestored(context.Background(), point, operation.RollbackResult{Restored: true})
	if err != nil || !verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
	record, _, _ := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	backup, _ := fixture.objects.Inspect(context.Background(), manifest.Plan.Pair.Target, "db", manifest.Baselines[0].Restore.Table)
	if record.State != RestoreCleaned || backup.Exists || fixture.stage.exists {
		t.Fatalf("record=%#v backup=%#v staging=%v", record, backup, fixture.stage.exists)
	}
}

func TestVerifiedRollbackRejectsCoherentlyTamperedManifestWithoutCleanup(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-manifest-record-binding")
	point := fixture.create(t)
	manifest, err := fixture.provider.decodeManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	item := manifest.Plan.Items[0]
	if err := fixture.ledger.Put(context.Background(), LedgerEntry{Key: ledgerKeyForChunk(item.Chunk), Strategy: item.Chunk.Strategy, State: LedgerRolledBack, Attempt: 1, StagingTable: item.Chunk.StagingTable, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	tampered := manifest
	tampered.Baselines = append([]ExchangeRestoreBaseline(nil), manifest.Baselines...)
	baseline := tampered.Baselines[0]
	baseline.OwnershipToken = strings.Repeat("10", restoreOwnershipTokenBytes)
	baseline.Restore.Table, err = BuildRestoreTableName(point.RunID, baseline.Key.Table, baseline.OwnershipToken)
	if err != nil {
		t.Fatal(err)
	}
	baseline.Restore.UUID = "generated-" + baseline.Restore.Table
	tampered.Baselines[0] = baseline
	fixture.objects.mu.Lock()
	fixture.objects.objects[baseline.Restore.Database+"."+baseline.Restore.Table] = RestoreObjectSnapshot{Exists: true, Identity: baseline.Restore, Partitions: append([]Partition(nil), baseline.Partitions...)}
	fixture.objects.fingerprints[baseline.Restore.Database+"."+baseline.Restore.Table] = baseline.Fingerprint
	fixture.objects.mu.Unlock()

	verification, verifyErr := fixture.provider.VerifyRestored(context.Background(), corruptRestoreManifest(t, point, tampered), operation.RollbackResult{Restored: true})
	if verifyErr == nil || !strings.Contains(verifyErr.Error(), "does not match the durable restore record") || verification.Passed {
		t.Fatalf("coherently tampered manifest accepted: verification=%#v err=%v", verification, verifyErr)
	}
	record, ok, recordErr := fixture.store.GetRestore(context.Background(), restoreKeyForItem(point.RunID, item))
	if recordErr != nil || !ok || record.State != RestoreReady || fixture.objects.drops != 0 {
		t.Fatalf("durable state changed after manifest tampering: record=%#v ok=%v err=%v drops=%d", record, ok, recordErr, fixture.objects.drops)
	}
	realObject, realErr := fixture.objects.Inspect(context.Background(), fixture.pair.Target, manifest.Baselines[0].Restore.Database, manifest.Baselines[0].Restore.Table)
	fakeObject, fakeErr := fixture.objects.Inspect(context.Background(), fixture.pair.Target, baseline.Restore.Database, baseline.Restore.Table)
	if realErr != nil || fakeErr != nil || !realObject.Exists || !fakeObject.Exists {
		t.Fatalf("restore objects changed after manifest tampering: real=%#v real_err=%v fake=%#v fake_err=%v", realObject, realErr, fakeObject, fakeErr)
	}
}

func TestMultiTableRestoreValidatesEveryDurableRecordBeforeMutation(t *testing.T) {
	ledger := newMemoryLedger()
	stage := &noOpStaging{exists: true}
	query := &commitQueryClient{}
	verifier := &restoreVerifier{fingerprint: DataFingerprint{}}
	commit, err := NewAtomicExchangeCommitEngine(ledger, query, verifier, allowCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	tables := []Table{
		{Database: "db", Name: "events", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}},
		{Database: "db", Name: "audit", Engine: "MergeTree", Columns: []Column{{Name: "id", Type: "UInt64"}}},
	}
	provider, _, _ := newTestRestoreProvider(t, ledger, stage, verifier, commit, tables...)
	items := make([]ExchangeRestoreItem, 0, len(tables))
	targets := make([]operation.Target, 0, len(tables))
	for index, table := range tables {
		staging, buildErr := BuildStagingTableName("prepared", table.Name)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		items = append(items, ExchangeRestoreItem{Chunk: TransferChunk{RunID: "prepared", Strategy: StrategyNativeStream, SourceDatabase: "db", SourceTable: table.Name, TargetDatabase: "db", TargetTable: table.Name, StagingTable: staging, Sequence: uint64(index + 1)}, TargetTable: table})
		targets = append(targets, operation.Target{Kind: operation.TargetDataObject, Component: "clickhouse", Resource: "db." + table.Name})
	}
	artifact, err := EncodeExchangeRestorePlan(ExchangeRestorePlan{Pair: PairParameters{Source: Endpoint{Host: "source", Port: 9000}, Target: Endpoint{Host: "target", Port: 9000}, Database: "db", Tables: []string{"events", "audit"}}, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	point, err := provider.Create(context.Background(), operation.RestorePointRequest{OperationID: OperationID, RunID: "run-multi-bind", Targets: targets, Plan: operation.Plan{Execution: artifact}, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := provider.decodeManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	first := manifest.Plan.Items[0]
	firstEntry := LedgerEntry{Key: ledgerKeyForChunk(first.Chunk), Strategy: first.Chunk.Strategy, State: LedgerStaging, Attempt: 1, StagingTable: first.Chunk.StagingTable, UpdatedAt: time.Now().UTC()}
	if err := ledger.Put(context.Background(), firstEntry); err != nil {
		t.Fatal(err)
	}

	tampered := manifest
	tampered.Baselines = append([]ExchangeRestoreBaseline(nil), manifest.Baselines...)
	tampered.Baselines[1].OwnershipToken = strings.Repeat("10", restoreOwnershipTokenBytes)
	tampered.Baselines[1].Restore.Table, err = BuildRestoreTableName(point.RunID, tampered.Baselines[1].Key.Table, tampered.Baselines[1].OwnershipToken)
	if err != nil {
		t.Fatal(err)
	}
	_, restoreErr := provider.Restore(context.Background(), corruptRestoreManifest(t, point, tampered), operation.ApplyResult{})
	if restoreErr == nil || !strings.Contains(restoreErr.Error(), "does not match the durable restore record") {
		t.Fatalf("multi-table manifest tampering accepted: %v", restoreErr)
	}
	stored, ok, getErr := ledger.Get(context.Background(), firstEntry.Key)
	if getErr != nil || !ok || stored.State != LedgerStaging || stage.drops != 0 || query.exchanges != 0 {
		t.Fatalf("first item mutated before full manifest binding: entry=%#v ok=%v err=%v drops=%d exchanges=%d", stored, ok, getErr, stage.drops, query.exchanges)
	}
	secondKey := restoreKeyForItem(point.RunID, manifest.Plan.Items[1])
	secondRecord, ok, getErr := provider.store.GetRestore(context.Background(), secondKey)
	if getErr != nil || !ok {
		t.Fatalf("second restore record missing: ok=%v err=%v", ok, getErr)
	}
	secondRecord.State = RestoreManualReview
	secondRecord.UpdatedAt = time.Now().UTC()
	if err := provider.store.PutRestore(context.Background(), secondRecord); err != nil {
		t.Fatal(err)
	}
	if cleanupErr := provider.cleanupVerifiedRestore(context.Background(), point.RunID, manifest); cleanupErr == nil || !strings.Contains(cleanupErr.Error(), "blocked in state manual_review") {
		t.Fatalf("multi-table cleanup accepted invalid later state: %v", cleanupErr)
	}
	firstRecord, ok, getErr := provider.store.GetRestore(context.Background(), restoreKeyForItem(point.RunID, first))
	if getErr != nil || !ok || firstRecord.State != RestoreReady || stage.drops != 0 {
		t.Fatalf("first item cleaned before all states were validated: record=%#v ok=%v err=%v drops=%d", firstRecord, ok, getErr, stage.drops)
	}
}

func TestRollbackVerificationFailurePreservesRestorePoint(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-rollback-drift")
	point := fixture.create(t)
	fixture.objects.setFingerprint("db", "events", DataFingerprint{Rows: 3, HashSum64: "30", HashXor64: "3"})
	verification, err := fixture.provider.VerifyRestored(context.Background(), point, operation.RollbackResult{Restored: true})
	if err != nil || verification.Passed {
		t.Fatalf("verification=%#v err=%v", verification, err)
	}
	manifest, _ := fixture.provider.decodeManifest(point)
	backup, _ := fixture.objects.Inspect(context.Background(), manifest.Plan.Pair.Target, "db", manifest.Baselines[0].Restore.Table)
	record, _, _ := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if !backup.Exists || record.State != RestoreReady {
		t.Fatalf("restore evidence removed after failed verification: backup=%#v record=%#v", backup, record)
	}
}

func TestCleanupReconcilesAfterFinalPersistenceFailure(t *testing.T) {
	fixture := newNonEmptyRestoreFixture(t, "run-cleanup-restart")
	point := fixture.create(t)
	manifest, _ := fixture.provider.decodeManifest(point)
	item := manifest.Plan.Items[0]
	if err := fixture.ledger.Put(context.Background(), LedgerEntry{Key: ledgerKeyForChunk(item.Chunk), Strategy: item.Chunk.Strategy, State: LedgerRolledBack, Attempt: 1, StagingTable: item.Chunk.StagingTable, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	fixture.store.failAt = fixture.store.puts + 2
	if _, err := fixture.provider.VerifyRestored(context.Background(), point, operation.RollbackResult{Restored: true}); err == nil || !strings.Contains(err.Error(), "persist verified") {
		t.Fatalf("expected final cleanup persistence failure, got %v", err)
	}
	verification, err := fixture.provider.VerifyRestored(context.Background(), point, operation.RollbackResult{Restored: true})
	if err != nil || !verification.Passed {
		t.Fatalf("cleanup reconciliation=%#v err=%v", verification, err)
	}
	record, _, _ := fixture.store.GetRestore(context.Background(), RestoreKey{RunID: fixture.request.RunID, Database: "db", Table: "events"})
	if record.State != RestoreCleaned || fixture.objects.drops != 1 {
		t.Fatalf("record=%#v drops=%d", record, fixture.objects.drops)
	}
}
