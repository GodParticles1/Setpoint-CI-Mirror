package operation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type lifecycleDefinition struct {
	verifyPass    bool
	applyCalls    int
	rollbackCalls int
}

func (definition *lifecycleDefinition) Metadata() Metadata {
	return Metadata{ID: "operation.test.lifecycle", Category: "test", Name: "Lifecycle test", Version: "1", Risk: RiskLow, SupportedSystems: []string{"linux"}}
}
func (definition *lifecycleDefinition) Discover(context.Context, DiscoverInput) (Discovery, error) {
	return Discovery{Applicable: true, Targets: []Target{{Kind: TargetDataObject, NodeID: "node-1", Component: "clickhouse", Resource: "db.events"}}, Snapshot: Artifact{SchemaVersion: "discover.v1", Payload: json.RawMessage(`{"ok":true}`)}}, nil
}
func (definition *lifecycleDefinition) Precheck(context.Context, PrecheckInput) (Precheck, error) {
	return Precheck{Passed: true, Snapshot: Artifact{SchemaVersion: "precheck.v1", Payload: json.RawMessage(`{"ok":true}`)}}, nil
}
func (definition *lifecycleDefinition) Plan(context.Context, PlanInput) (Plan, error) {
	return Plan{SchemaVersion: "plan.v1", Execution: Artifact{SchemaVersion: "execution.v1", Payload: json.RawMessage(`{"step":1}`)}}, nil
}
func (definition *lifecycleDefinition) Impact(context.Context, ImpactInput) (Impact, error) {
	return Impact{Risk: RiskLow}, nil
}
func (definition *lifecycleDefinition) Apply(context.Context, ApplyInput) (ApplyResult, error) {
	definition.applyCalls++
	return ApplyResult{Changed: true, Checkpoint: "applied", State: Artifact{SchemaVersion: "apply.v1", Payload: json.RawMessage(`{"changed":true}`)}}, nil
}
func (definition *lifecycleDefinition) Verify(context.Context, VerifyInput) (Verification, error) {
	return Verification{Passed: definition.verifyPass}, nil
}
func (definition *lifecycleDefinition) Rollback(context.Context, RollbackInput) (RollbackResult, error) {
	definition.rollbackCalls++
	return RollbackResult{Restored: true, Checkpoint: "restored", State: Artifact{SchemaVersion: "rollback.v1", Payload: json.RawMessage(`{"restored":true}`)}}, nil
}
func (definition *lifecycleDefinition) VerifyRollback(context.Context, VerifyRollbackInput) (Verification, error) {
	return Verification{Passed: true}, nil
}

type lifecycleLocks struct {
	mu       sync.Mutex
	lease    LockLease
	released int
}

func (locks *lifecycleLocks) Acquire(_ context.Context, request LockRequest) (LockLease, error) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	now := time.Now().UTC()
	locks.lease = LockLease{ID: "lease-1", OwnerID: request.OwnerID, Resources: append([]LockResource(nil), request.Resources...), AcquiredAt: now, ExpiresAt: now.Add(request.TTL)}
	return locks.lease, nil
}
func (locks *lifecycleLocks) Renew(_ context.Context, lease LockLease, ttl time.Duration) (LockLease, error) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	lease.ExpiresAt = time.Now().UTC().Add(ttl)
	locks.lease = lease
	return lease, nil
}
func (locks *lifecycleLocks) Release(context.Context, LockLease) error {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	locks.released++
	return nil
}

type lifecycleJournal struct {
	entries   []JournalEntry
	failState State
}

func (journal *lifecycleJournal) Append(_ context.Context, entry JournalEntry) error {
	if entry.State == journal.failState {
		return errors.New("injected operation journal failure")
	}
	journal.entries = append(journal.entries, entry)
	return nil
}
func (journal *lifecycleJournal) List(_ context.Context, runID string) ([]JournalEntry, error) {
	out := make([]JournalEntry, 0)
	for _, entry := range journal.entries {
		if entry.RunID == runID {
			out = append(out, entry)
		}
	}
	return out, nil
}

type lifecycleRestore struct {
	verifyPass   bool
	restoredPass bool
}

func (restore *lifecycleRestore) ID() string { return "restore.test" }
func (restore *lifecycleRestore) Create(_ context.Context, request RestorePointRequest) (RestorePoint, error) {
	now := time.Now().UTC()
	return RestorePoint{ID: "rp-1", ProviderID: restore.ID(), OperationID: request.OperationID, RunID: request.RunID, Status: RestorePointCreated, Targets: append([]Target(nil), request.Targets...), CreatedAt: now, Manifest: Artifact{SchemaVersion: "restore.v1", Payload: json.RawMessage(`{"baseline":true}`)}}, nil
}
func (restore *lifecycleRestore) Verify(context.Context, RestorePoint) (Verification, error) {
	return Verification{Passed: restore.verifyPass}, nil
}
func (restore *lifecycleRestore) Restore(context.Context, RestorePoint, ApplyResult) (RollbackResult, error) {
	return RollbackResult{}, errors.New("coordinator must use operation rollback contract")
}
func (restore *lifecycleRestore) VerifyRestored(context.Context, RestorePoint, RollbackResult) (Verification, error) {
	return Verification{Passed: restore.restoredPass}, nil
}

func newLifecycleCoordinator(t *testing.T, verifyPass bool) (*Coordinator, *lifecycleDefinition, *lifecycleLocks, *lifecycleJournal) {
	t.Helper()
	locks := &lifecycleLocks{}
	journal := &lifecycleJournal{}
	restore := &lifecycleRestore{verifyPass: true, restoredPass: true}
	coordinator, err := NewCoordinator(locks, journal, restore)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.lockTTL = time.Hour
	definition := &lifecycleDefinition{verifyPass: verifyPass}
	return coordinator, definition, locks, journal
}

func TestCoordinatorHappyPathUsesLockRestoreApplyVerifyAndRelease(t *testing.T) {
	coordinator, definition, locks, journal := newLifecycleCoordinator(t, true)
	prepared, err := coordinator.Prepare(context.Background(), "run-1", definition, RuntimeInput{System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != StateAwaitingConfirm {
		t.Fatalf("prepared state=%s", prepared.State)
	}
	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateSucceeded || definition.applyCalls != 1 || definition.rollbackCalls != 0 || locks.released != 1 {
		t.Fatalf("result=%#v apply=%d rollback=%d released=%d", result, definition.applyCalls, definition.rollbackCalls, locks.released)
	}
	if len(journal.entries) < 8 || journal.entries[len(journal.entries)-1].State != StateSucceeded {
		t.Fatalf("journal=%#v", journal.entries)
	}
}

func TestCoordinatorVerificationFailureRollsBackAndVerifies(t *testing.T) {
	coordinator, definition, locks, _ := newLifecycleCoordinator(t, false)
	prepared, err := coordinator.Prepare(context.Background(), "run-2", definition, RuntimeInput{System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err == nil {
		t.Fatal("verification failure unexpectedly returned nil error")
	}
	if result.State != StateRolledBack || definition.rollbackCalls != 1 || !result.RollbackVerification.Passed || locks.released != 1 {
		t.Fatalf("result=%#v rollback=%d released=%d", result, definition.rollbackCalls, locks.released)
	}
}

func TestCoordinatorBlocksBeforeApplyWhenRestorePointCannotVerify(t *testing.T) {
	locks := &lifecycleLocks{}
	journal := &lifecycleJournal{}
	restore := &lifecycleRestore{verifyPass: false, restoredPass: true}
	coordinator, err := NewCoordinator(locks, journal, restore)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.lockTTL = time.Hour
	definition := &lifecycleDefinition{verifyPass: true}
	prepared, err := coordinator.Prepare(context.Background(), "run-3", definition, RuntimeInput{System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err == nil {
		t.Fatal("unverified restore point unexpectedly allowed")
	}
	if result.State != StateBlocked || definition.applyCalls != 0 || locks.released != 1 {
		t.Fatalf("result=%#v apply=%d released=%d", result, definition.applyCalls, locks.released)
	}
}

func TestCoordinatorReportsTerminalJournalPersistenceFailure(t *testing.T) {
	coordinator, definition, _, journal := newLifecycleCoordinator(t, true)
	journal.failState = StateSucceeded
	prepared, err := coordinator.Prepare(context.Background(), "run-journal-fail", definition, RuntimeInput{System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err == nil || !strings.Contains(err.Error(), "append operation journal") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.State != StateVerifying || definition.applyCalls != 1 {
		t.Fatalf("result=%#v apply=%d", result, definition.applyCalls)
	}
}

func TestCoordinatorReportsRollbackFailureJournalPersistenceFailure(t *testing.T) {
	coordinator, definition, _, journal := newLifecycleCoordinator(t, false)
	journal.failState = StateRolledBack
	prepared, err := coordinator.Prepare(context.Background(), "run-rollback-journal-fail", definition, RuntimeInput{System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ExecuteConfirmed(context.Background(), prepared, definition)
	if err == nil || !strings.Contains(err.Error(), "append operation journal") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.State != StateRollingBack || definition.rollbackCalls != 1 {
		t.Fatalf("result=%#v rollback=%d", result, definition.rollbackCalls)
	}
}
