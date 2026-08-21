package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/storage/sqlite"
)

const l3aPhysicalFlag = "SETPOINT_L3A_PHYSICAL"

var l3aDatabasePattern = regexp.MustCompile(`^sp_lab_l3a_[A-Za-z0-9_]{1,48}$`)

type l3aConfig struct {
	RunID      string
	Database   string
	WorkDir    string
	Client     clickhouse.ClientCommand
	SourcePort uint16
	TargetPort uint16
}

type l3aJournal struct {
	mu      sync.Mutex
	entries []operation.JournalEntry
}

func (journal *l3aJournal) Append(_ context.Context, entry operation.JournalEntry) error {
	if err := operation.ValidateJournalEntry(entry); err != nil {
		return err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if len(journal.entries) > 0 && entry.Sequence != journal.entries[len(journal.entries)-1].Sequence+1 {
		return fmt.Errorf("L3-A journal sequence is not contiguous: %d", entry.Sequence)
	}
	journal.entries = append(journal.entries, entry)
	return nil
}

func (journal *l3aJournal) List(_ context.Context, runID string) ([]operation.JournalEntry, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	result := make([]operation.JournalEntry, 0, len(journal.entries))
	for _, entry := range journal.entries {
		if entry.RunID == runID {
			result = append(result, entry)
		}
	}
	return result, nil
}

type l3aLockManager struct {
	mu       sync.Mutex
	lease    operation.LockLease
	released bool
}

func (manager *l3aLockManager) Acquire(_ context.Context, request operation.LockRequest) (operation.LockLease, error) {
	normalized, err := operation.NormalizeLockRequest(request)
	if err != nil {
		return operation.LockLease{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.lease.ID != "" && !manager.released {
		return operation.LockLease{}, errors.New("L3-A lock is already held")
	}
	now := time.Now().UTC()
	manager.lease = operation.LockLease{ID: "l3a-lease-1", OwnerID: normalized.OwnerID, Resources: normalized.Resources, AcquiredAt: now, ExpiresAt: now.Add(normalized.TTL)}
	manager.released = false
	return manager.lease, nil
}

func (manager *l3aLockManager) Renew(_ context.Context, lease operation.LockLease, ttl time.Duration) (operation.LockLease, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.released || lease.ID != manager.lease.ID {
		return operation.LockLease{}, errors.New("L3-A lock is not held")
	}
	manager.lease.ExpiresAt = time.Now().UTC().Add(ttl)
	return manager.lease, nil
}

func (manager *l3aLockManager) Release(_ context.Context, lease operation.LockLease) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.released || lease.ID != manager.lease.ID {
		return errors.New("L3-A lock release does not match the held lease")
	}
	manager.released = true
	return nil
}

type l3aLeaseHandle struct{ lease operation.LockLease }

func (handle l3aLeaseHandle) Current() operation.LockLease { return handle.lease }
func (handle l3aLeaseHandle) Validate(now time.Time) error {
	return operation.ValidateLease(handle.lease, now)
}

type l3aUnusedCommitGuard struct{}

func (l3aUnusedCommitGuard) Verify(context.Context, clickhouse.CommitGuardRequest) error {
	return errors.New("L3-A restore provider must not issue a separate commit")
}

type l3aEvidence struct {
	RunID                     string                     `json:"run_id"`
	Database                  string                     `json:"database"`
	Source                    clickhouse.DataFingerprint `json:"source_before"`
	TargetOriginal            clickhouse.DataFingerprint `json:"target_original"`
	RestorePoint              clickhouse.DataFingerprint `json:"restore_point"`
	TargetAfterApply          clickhouse.DataFingerprint `json:"target_after_apply"`
	TargetAfterRollback       clickhouse.DataFingerprint `json:"target_after_rollback"`
	RestoreStateBeforeCleanup clickhouse.RestoreState    `json:"restore_state_before_cleanup"`
	RestoreStateAfterCleanup  clickhouse.RestoreState    `json:"restore_state_after_cleanup"`
	Ledger                    []clickhouse.LedgerEntry   `json:"ledger"`
	Journal                   []operation.JournalEntry   `json:"journal"`
	SQLiteIntegrity           string                     `json:"sqlite_integrity"`
	Cleanup                   map[string]bool            `json:"cleanup"`
	ProductApply              string                     `json:"product_apply"`
}

func TestClickHouseL3APhysicalLifecycle(t *testing.T) {
	if os.Getenv(l3aPhysicalFlag) != "1" {
		t.Skip("L3-A physical lifecycle requires explicit test environment configuration")
	}
	config := readL3AConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	commandExecutor, err := executor.NewOSExecutor(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	client, err := clickhouse.NewExecutorClientWithCommand(commandExecutor, config.Client)
	if err != nil {
		t.Fatal(err)
	}
	source := clickhouse.Endpoint{Host: "127.0.0.1", Port: config.SourcePort}
	target := clickhouse.Endpoint{Host: "127.0.0.1", Port: config.TargetPort}
	assertDatabaseAbsent(t, ctx, client, source, config.Database)
	assertDatabaseAbsent(t, ctx, client, target, config.Database)
	cleanupRequired := true
	defer func() {
		if cleanupRequired {
			if cleanupErr := dropL3AFixture(client, source, target, config.Database); cleanupErr != nil {
				t.Errorf("L3-A failure cleanup: %v", cleanupErr)
			}
		}
	}()
	createL3AFixture(t, ctx, client, source, target, config.Database)

	storePath := filepath.Join(config.WorkDir, "l3a.sqlite")
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := clickhouse.NewQueryFingerprintVerifier(client)
	if err != nil {
		t.Fatal(err)
	}
	staging, err := clickhouse.NewSQLStagingController(client)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := clickhouse.NewPipelineNativeTransportWithCommands(commandExecutor, config.Client, config.Client)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := clickhouse.NewDefinition(client, store, staging, transport, verifier)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := clickhouse.NewSQLRestoreObjectController(client)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := clickhouse.NewAtomicExchangeCommitEngine(store, client, verifier, l3aUnusedCommitGuard{})
	if err != nil {
		t.Fatal(err)
	}
	restore, err := clickhouse.NewExchangeRestoreProvider(store, store, staging, objects, verifier, commit)
	if err != nil {
		t.Fatal(err)
	}

	journal := &l3aJournal{}
	locks := &l3aLockManager{}
	coordinator, err := operation.NewCoordinator(locks, journal, restore)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := json.Marshal(clickhouse.PairParameters{Source: source, Target: target, Database: config.Database, Tables: []string{"events"}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := coordinator.Prepare(ctx, config.RunID, definition, operation.RuntimeInput{System: "linux", Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != operation.StateAwaitingConfirm || !prepared.Precheck.Passed {
		t.Fatalf("L3-A preparation did not reach awaiting_confirmation: state=%s precheck=%#v", prepared.State, prepared.Precheck)
	}
	if !hasFinding(prepared.Precheck.Findings, "TARGET_NONEMPTY_RESTORE_REQUIRED", operation.FindingWarning) {
		t.Fatalf("L3-A non-empty restore requirement is missing: %#v", prepared.Precheck.Findings)
	}

	table := l3ATable()
	sourceBefore := fingerprint(t, ctx, verifier, source, config.Database, table)
	targetBefore := fingerprint(t, ctx, verifier, target, config.Database, table)
	if sourceBefore.Rows != 16 || targetBefore.Rows != 4 || clickhouse.CompareFingerprints(sourceBefore, targetBefore).Passed {
		t.Fatalf("unexpected L3-A fixture fingerprints: source=%#v target=%#v", sourceBefore, targetBefore)
	}

	state := prepared.State
	sequence := prepared.JournalSequence
	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateQueued, "L3-A confirmed lab execution", "confirmed")
	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateAcquiringLock, "acquire L3-A target lock", "acquire_lock")
	resourceKey, err := operation.ResourceLockKey(prepared.Discovery.Targets[0])
	if err != nil {
		t.Fatal(err)
	}
	lease, err := locks.Acquire(ctx, operation.LockRequest{OwnerID: config.RunID, Resources: []operation.LockResource{{Key: resourceKey}}, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	handle := l3aLeaseHandle{lease: lease}

	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateCreatingRestorePoint, "persist, create and verify L3-A restore point", "restore_point")
	point, err := restore.Create(ctx, operation.RestorePointRequest{OperationID: clickhouse.OperationID, RunID: config.RunID, Targets: prepared.Discovery.Targets, Plan: prepared.Plan, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	restoreVerification, err := restore.Verify(ctx, point)
	if err != nil || !restoreVerification.Passed {
		t.Fatalf("restore point verification=%#v err=%v", restoreVerification, err)
	}
	records, err := store.ListRestores(ctx, config.RunID)
	if err != nil || len(records) != 1 || records[0].State != clickhouse.RestoreReady {
		t.Fatalf("restore records=%#v err=%v", records, err)
	}
	restoreFingerprint := fingerprint(t, ctx, verifier, target, config.Database, restoreTableFor(t, records[0], table))
	if !clickhouse.CompareFingerprints(targetBefore, restoreFingerprint).Passed {
		t.Fatalf("restore fingerprint=%#v target original=%#v", restoreFingerprint, targetBefore)
	}

	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateRunning, "L3-A bounded Apply", "apply")
	apply, err := definition.Apply(ctx, operation.ApplyInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Impact: prepared.Impact, RestorePoint: point, Lease: handle})
	if err != nil {
		t.Fatal(err)
	}
	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateVerifying, "verify L3-A applied source state", "verify")
	applyVerification, err := definition.Verify(ctx, operation.VerifyInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Apply: apply})
	if err != nil || !applyVerification.Passed {
		t.Fatalf("Apply verification=%#v err=%v", applyVerification, err)
	}
	targetAfterApply := fingerprint(t, ctx, verifier, target, config.Database, table)
	if !clickhouse.CompareFingerprints(sourceBefore, targetAfterApply).Passed {
		t.Fatalf("target after Apply=%#v source=%#v", targetAfterApply, sourceBefore)
	}

	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateRollingBack, "L3-A rollback intent and guarded reverse exchange", "rollback_intent")
	rollback, err := definition.Rollback(ctx, operation.RollbackInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Apply: apply, RestorePoint: point, Lease: handle})
	if err != nil {
		t.Fatal(err)
	}
	rollbackVerification, err := definition.VerifyRollback(ctx, operation.VerifyRollbackInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Rollback: rollback, RestorePoint: point})
	if err != nil || !rollbackVerification.Passed {
		t.Fatalf("plugin rollback verification=%#v err=%v", rollbackVerification, err)
	}
	records, err = store.ListRestores(ctx, config.RunID)
	if err != nil || len(records) != 1 || records[0].State != clickhouse.RestoreReady {
		t.Fatalf("restore object was not preserved through rollback verification: records=%#v err=%v", records, err)
	}
	if exists := restoreObjectExists(t, ctx, objects, target, records[0]); !exists {
		t.Fatal("restore object was removed before VerifyRestored")
	}
	restoreStateBeforeCleanup := records[0].State
	restoredVerification, err := restore.VerifyRestored(ctx, point, rollback)
	if err != nil || !restoredVerification.Passed {
		t.Fatalf("restore rollback verification=%#v err=%v", restoredVerification, err)
	}
	advanceL3A(t, journal, config.RunID, &state, &sequence, operation.StateRolledBack, "L3-A rollback and cleanup verified", "rollback_verified")
	if err := locks.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	targetAfterRollback := fingerprint(t, ctx, verifier, target, config.Database, table)
	if !clickhouse.CompareFingerprints(targetBefore, targetAfterRollback).Passed {
		t.Fatalf("target after rollback=%#v original=%#v", targetAfterRollback, targetBefore)
	}
	records, err = store.ListRestores(ctx, config.RunID)
	if err != nil || len(records) != 1 || records[0].State != clickhouse.RestoreCleaned {
		t.Fatalf("restore cleanup records=%#v err=%v", records, err)
	}
	restoreStateAfterCleanup := records[0].State
	ledger, err := store.ListRun(ctx, config.RunID)
	if err != nil || len(ledger) != 1 || ledger[0].State != clickhouse.LedgerRolledBack {
		t.Fatalf("ledger=%#v err=%v", ledger, err)
	}
	if restoreObjectExists(t, ctx, objects, target, records[0]) {
		t.Fatal("restore object remains after verified cleanup")
	}
	assertRunOwnedObjectsAbsent(t, ctx, client, target, config.Database)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	integrity := sqliteIntegrity(t, storePath)
	if err := dropL3AFixture(client, source, target, config.Database); err != nil {
		t.Fatal(err)
	}
	cleanupRequired = false
	entries, err := journal.List(ctx, config.RunID)
	if err != nil {
		t.Fatal(err)
	}
	evidence := l3aEvidence{
		RunID: config.RunID, Database: config.Database, Source: sourceBefore, TargetOriginal: targetBefore,
		RestorePoint: restoreFingerprint, TargetAfterApply: targetAfterApply, TargetAfterRollback: targetAfterRollback,
		RestoreStateBeforeCleanup: restoreStateBeforeCleanup, RestoreStateAfterCleanup: restoreStateAfterCleanup,
		Ledger: ledger, Journal: entries, SQLiteIntegrity: integrity,
		Cleanup:      map[string]bool{"source_database_absent": true, "target_database_absent": true, "run_owned_objects_absent": true, "lock_released": locks.released},
		ProductApply: "disabled; physical execution is an explicitly gated test-only lifecycle",
	}
	writeL3AEvidence(t, filepath.Join(config.WorkDir, "l3a-evidence.json"), evidence)
}

func readL3AConfig(t *testing.T) l3aConfig {
	t.Helper()
	config := l3aConfig{RunID: strings.TrimSpace(os.Getenv("SETPOINT_L3A_RUN_ID")), Database: strings.TrimSpace(os.Getenv("SETPOINT_L3A_DATABASE")), WorkDir: strings.TrimSpace(os.Getenv("SETPOINT_L3A_WORK_DIR"))}
	if runtime.GOOS != "linux" {
		t.Fatal("L3-A physical execution is Linux-only")
	}
	if config.RunID == "" || !l3aDatabasePattern.MatchString(config.Database) || !filepath.IsAbs(config.WorkDir) || !strings.HasPrefix(filepath.Clean(config.WorkDir), "/tmp/setpoint-l3a-") {
		t.Fatal("L3-A requires a run ID, bounded sp_lab_l3a_* database and /tmp/setpoint-l3a-* work directory")
	}
	config.SourcePort = parseL3APort(t, "SETPOINT_L3A_SOURCE_PORT")
	config.TargetPort = parseL3APort(t, "SETPOINT_L3A_TARGET_PORT")
	if config.SourcePort == config.TargetPort {
		t.Fatal("L3-A source and target ports must differ")
	}
	clientPath := filepath.Clean(strings.TrimSpace(os.Getenv("SETPOINT_L3A_CLIENT")))
	if !filepath.IsAbs(clientPath) {
		t.Fatal("L3-A ClickHouse client path must be absolute")
	}
	switch filepath.Base(clientPath) {
	case "clickhouse-client":
		config.Client = clickhouse.ClassicClientCommand(clientPath)
	case "clickhouse":
		config.Client = clickhouse.UnifiedClientCommand(clientPath)
	default:
		t.Fatalf("unsupported L3-A ClickHouse client %q", filepath.Base(clientPath))
	}
	return config
}

func parseL3APort(t *testing.T, name string) uint16 {
	t.Helper()
	parsed, err := strconv.ParseUint(strings.TrimSpace(os.Getenv(name)), 10, 16)
	if err != nil || parsed == 0 {
		t.Fatalf("%s must be a valid non-zero port", name)
	}
	return uint16(parsed)
}

func createL3AFixture(t *testing.T, ctx context.Context, client clickhouse.QueryClient, source, target clickhouse.Endpoint, database string) {
	t.Helper()
	for _, endpoint := range []clickhouse.Endpoint{source, target} {
		queryL3A(t, ctx, client, endpoint, "default", "CREATE DATABASE "+quoteL3A(database)+" ENGINE = Atomic", clickhouse.FormatTSVRaw)
		queryL3A(t, ctx, client, endpoint, database, "CREATE TABLE "+quoteL3A(database)+".`events` (`id` UInt64, `payload` String) ENGINE = MergeTree ORDER BY `id`", clickhouse.FormatTSVRaw)
	}
	queryL3A(t, ctx, client, source, database, "INSERT INTO "+quoteL3A(database)+".`events` SELECT number, concat('source-', toString(number)) FROM numbers(16)", clickhouse.FormatTSVRaw)
	queryL3A(t, ctx, client, target, database, "INSERT INTO "+quoteL3A(database)+".`events` SELECT number + 1000, concat('target-', toString(number)) FROM numbers(4)", clickhouse.FormatTSVRaw)
}

func dropL3AFixture(client clickhouse.QueryClient, source, target clickhouse.Endpoint, database string) error {
	var cleanupErrs []error
	for _, endpoint := range []clickhouse.Endpoint{source, target} {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 20*time.Second)
		_, dropErr := client.Query(dropCtx, clickhouse.QueryRequest{Host: endpoint.Host, Port: endpoint.Port, Database: "default", Query: "DROP DATABASE IF EXISTS " + quoteL3A(database), Format: clickhouse.FormatTSVRaw})
		dropCancel()
		if dropErr != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("drop L3-A database on %s:%d: %w", endpoint.Host, endpoint.Port, dropErr))
		}
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 20*time.Second)
		result, err := client.Query(verifyCtx, clickhouse.QueryRequest{Host: endpoint.Host, Port: endpoint.Port, Database: "default", Query: "SELECT count() FROM system.databases WHERE name = '" + database + "'", Format: clickhouse.FormatTSVRaw})
		verifyCancel()
		if err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("verify L3-A database cleanup on %s:%d: %w", endpoint.Host, endpoint.Port, err))
		} else if strings.TrimSpace(result) != "0" {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("L3-A database %s remains on %s:%d", database, endpoint.Host, endpoint.Port))
		}
	}
	return errors.Join(cleanupErrs...)
}

func assertDatabaseAbsent(t *testing.T, ctx context.Context, client clickhouse.QueryClient, endpoint clickhouse.Endpoint, database string) {
	t.Helper()
	result := queryL3A(t, ctx, client, endpoint, "default", "SELECT count() FROM system.databases WHERE name = '"+database+"'", clickhouse.FormatTSVRaw)
	if result != "0" {
		t.Fatalf("L3-A database %s already exists or remains on %s:%d", database, endpoint.Host, endpoint.Port)
	}
}

func assertRunOwnedObjectsAbsent(t *testing.T, ctx context.Context, client clickhouse.QueryClient, endpoint clickhouse.Endpoint, database string) {
	t.Helper()
	result := queryL3A(t, ctx, client, endpoint, database, "SELECT count() FROM system.tables WHERE database = '"+database+"' AND (startsWith(name, 'spmig_') OR startsWith(name, 'sprp_'))", clickhouse.FormatTSVRaw)
	if result != "0" {
		t.Fatalf("run-owned ClickHouse objects remain: %s", result)
	}
}

func queryL3A(t *testing.T, ctx context.Context, client clickhouse.QueryClient, endpoint clickhouse.Endpoint, database, query string, format clickhouse.QueryFormat) string {
	t.Helper()
	result, err := client.Query(ctx, clickhouse.QueryRequest{Host: endpoint.Host, Port: endpoint.Port, Database: database, Query: query, Format: format})
	if err != nil {
		t.Fatalf("L3-A ClickHouse query failed: %v", err)
	}
	return strings.TrimSpace(result)
}

func l3ATable() clickhouse.Table {
	return clickhouse.Table{Database: "placeholder", Name: "events", Engine: "MergeTree", Columns: []clickhouse.Column{{Name: "id", Position: 1, Type: "UInt64"}, {Name: "payload", Position: 2, Type: "String"}}}
}

func restoreTableFor(t *testing.T, record clickhouse.RestoreRecord, table clickhouse.Table) clickhouse.Table {
	t.Helper()
	if record.Restore.Table == "" {
		t.Fatal("restore table identity is missing")
	}
	table.Name = record.Restore.Table
	table.Database = record.Restore.Database
	return table
}

func fingerprint(t *testing.T, ctx context.Context, verifier clickhouse.FingerprintVerifier, endpoint clickhouse.Endpoint, database string, table clickhouse.Table) clickhouse.DataFingerprint {
	t.Helper()
	table.Database = database
	value, err := verifier.Fingerprint(ctx, endpoint, database, table, nil)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func restoreObjectExists(t *testing.T, ctx context.Context, objects clickhouse.RestoreObjectController, endpoint clickhouse.Endpoint, record clickhouse.RestoreRecord) bool {
	t.Helper()
	snapshot, err := objects.Inspect(ctx, endpoint, record.Restore.Database, record.Restore.Table)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Exists
}

func hasFinding(findings []operation.Finding, code string, severity operation.FindingSeverity) bool {
	for _, finding := range findings {
		if finding.Code == code && finding.Severity == severity {
			return true
		}
	}
	return false
}

func advanceL3A(t *testing.T, journal *l3aJournal, runID string, state *operation.State, sequence *int64, next operation.State, message, checkpoint string) {
	t.Helper()
	if err := operation.Transition(*state, next); err != nil {
		t.Fatal(err)
	}
	entry := operation.JournalEntry{RunID: runID, Sequence: *sequence + 1, State: next, Checkpoint: checkpoint, Message: message, At: time.Now().UTC()}
	if err := journal.Append(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	*state = next
	*sequence++
}

func sqliteIntegrity(t *testing.T, path string) string {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var result string
	if err := database.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("SQLite integrity=%q", result)
	}
	return result
}

func writeL3AEvidence(t *testing.T, path string, evidence l3aEvidence) {
	t.Helper()
	payload, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func quoteL3A(identifier string) string { return "`" + identifier + "`" }

func TestL3APhysicalDatabaseScope(t *testing.T) {
	for value, allowed := range map[string]bool{
		"sp_lab_l3a_fixture": true,
		"sp_lab_l3a_":        false,
		"sp_lab_l3a_x;DROP":  false,
		"business":           false,
	} {
		if got := l3aDatabasePattern.MatchString(value); got != allowed {
			t.Fatalf("database=%q allowed=%t want=%t", value, got, allowed)
		}
	}
}

type l3aCleanupClient struct {
	requests []clickhouse.QueryRequest
}

func (client *l3aCleanupClient) Query(_ context.Context, request clickhouse.QueryRequest) (string, error) {
	client.requests = append(client.requests, request)
	if request.Port == 19000 && strings.HasPrefix(request.Query, "DROP DATABASE") {
		return "", errors.New("injected source cleanup failure")
	}
	if strings.HasPrefix(request.Query, "SELECT count()") {
		return "0", nil
	}
	return "", nil
}

func TestL3AFailureCleanupAttemptsBothIsolatedEndpoints(t *testing.T) {
	client := &l3aCleanupClient{}
	err := dropL3AFixture(client,
		clickhouse.Endpoint{Host: "127.0.0.1", Port: 19000},
		clickhouse.Endpoint{Host: "127.0.0.1", Port: 19001},
		"sp_lab_l3a_cleanup")
	if err == nil || !strings.Contains(err.Error(), "injected source cleanup failure") {
		t.Fatalf("cleanup error=%v", err)
	}
	seen := make(map[uint16]map[string]bool)
	for _, request := range client.requests {
		if seen[request.Port] == nil {
			seen[request.Port] = make(map[string]bool)
		}
		switch {
		case strings.HasPrefix(request.Query, "DROP DATABASE IF EXISTS"):
			seen[request.Port]["drop"] = true
		case strings.HasPrefix(request.Query, "SELECT count()"):
			seen[request.Port]["verify"] = true
		}
	}
	for _, port := range []uint16{19000, 19001} {
		if !seen[port]["drop"] || !seen[port]["verify"] {
			t.Fatalf("cleanup did not drop and verify port %d: %#v", port, client.requests)
		}
	}
}
