package operation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultOperationLockTTL          = 10 * time.Minute
	defaultRestorePointRetention     = 24 * time.Hour
	defaultRollbackVerificationLimit = 10 * time.Minute
)

type PreparedRun struct {
	RunID           string       `json:"run_id"`
	Metadata        Metadata     `json:"metadata"`
	Runtime         RuntimeInput `json:"-"`
	Discovery       Discovery    `json:"discovery"`
	Precheck        Precheck     `json:"precheck"`
	Plan            Plan         `json:"plan"`
	Impact          Impact       `json:"impact"`
	State           State        `json:"state"`
	JournalSequence int64        `json:"journal_sequence"`
}

type LifecycleResult struct {
	RunID                string         `json:"run_id"`
	State                State          `json:"state"`
	RestorePoint         RestorePoint   `json:"restore_point,omitempty"`
	Apply                ApplyResult    `json:"apply,omitempty"`
	Verification         Verification   `json:"verification,omitempty"`
	Rollback             RollbackResult `json:"rollback,omitempty"`
	RollbackVerification Verification   `json:"rollback_verification,omitempty"`
}

type Coordinator struct {
	locks            LockManager
	journal          Journal
	restore          RestorePointProvider
	lockTTL          time.Duration
	restoreRetention time.Duration
	rollbackTimeout  time.Duration
	now              func() time.Time
}

func NewCoordinator(locks LockManager, journal Journal, restore RestorePointProvider) (*Coordinator, error) {
	if locks == nil || journal == nil || restore == nil {
		return nil, errors.New("lock manager, journal and restore point provider are required")
	}
	return &Coordinator{
		locks:            locks,
		journal:          journal,
		restore:          restore,
		lockTTL:          defaultOperationLockTTL,
		restoreRetention: defaultRestorePointRetention,
		rollbackTimeout:  defaultRollbackVerificationLimit,
		now:              func() time.Time { return time.Now().UTC() },
	}, nil
}

func (coordinator *Coordinator) Prepare(ctx context.Context, runID string, definition OperationDefinition, runtime RuntimeInput) (PreparedRun, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return PreparedRun{}, errors.New("operation run ID is required")
	}
	if definition == nil {
		return PreparedRun{}, errors.New("operation definition is required")
	}
	metadata := definition.Metadata()
	if err := ValidateMetadata(metadata); err != nil {
		return PreparedRun{}, fmt.Errorf("validate operation metadata: %w", err)
	}

	prepared := PreparedRun{RunID: runID, Metadata: metadata, Runtime: runtime, State: StateDraft}
	if err := coordinator.advance(ctx, &prepared.State, &prepared.JournalSequence, runID, StateDiscovering, "discover operation targets", "discover"); err != nil {
		return prepared, err
	}
	discovery, err := definition.Discover(ctx, DiscoverInput{Runtime: runtime})
	if err != nil {
		return coordinator.prepareFailure(ctx, prepared, err)
	}
	prepared.Discovery = discovery
	if !discovery.Applicable {
		if err := coordinator.advance(ctx, &prepared.State, &prepared.JournalSequence, runID, StateBlocked, "operation is not applicable", "discover_blocked"); err != nil {
			return prepared, err
		}
		return prepared, nil
	}
	if len(discovery.Targets) == 0 {
		return coordinator.prepareFailure(ctx, prepared, errors.New("operation discovery returned no targets"))
	}
	prepared.Runtime.Targets = append([]Target(nil), discovery.Targets...)

	if err := coordinator.advance(ctx, &prepared.State, &prepared.JournalSequence, runID, StatePrechecking, "precheck discovered targets", "precheck"); err != nil {
		return prepared, err
	}
	precheck, err := definition.Precheck(ctx, PrecheckInput{Runtime: prepared.Runtime, Discovery: discovery})
	if err != nil {
		return coordinator.prepareFailure(ctx, prepared, err)
	}
	prepared.Precheck = precheck
	if !precheck.Passed {
		if err := coordinator.advance(ctx, &prepared.State, &prepared.JournalSequence, runID, StateBlocked, "precheck has blocking findings", "precheck_blocked"); err != nil {
			return prepared, err
		}
		return prepared, nil
	}

	plan, err := definition.Plan(ctx, PlanInput{Runtime: prepared.Runtime, Discovery: discovery, Precheck: precheck})
	if err != nil {
		return coordinator.prepareFailure(ctx, prepared, err)
	}
	prepared.Plan = plan
	if strings.TrimSpace(plan.SchemaVersion) == "" || strings.TrimSpace(plan.Execution.SchemaVersion) == "" || len(plan.Execution.Payload) == 0 {
		return coordinator.prepareFailure(ctx, prepared, errors.New("operation plan is missing a durable execution artifact"))
	}
	if err := coordinator.advance(ctx, &prepared.State, &prepared.JournalSequence, runID, StatePlanned, "operation plan created", "planned"); err != nil {
		return prepared, err
	}
	impact, err := definition.Impact(ctx, ImpactInput{Runtime: prepared.Runtime, Plan: plan})
	if err != nil {
		return coordinator.prepareFailure(ctx, prepared, err)
	}
	prepared.Impact = impact
	if err := coordinator.advance(ctx, &prepared.State, &prepared.JournalSequence, runID, StateAwaitingConfirm, "plan and impact await explicit confirmation", "awaiting_confirmation"); err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (coordinator *Coordinator) ExecuteConfirmed(ctx context.Context, prepared PreparedRun, definition OperationDefinition) (LifecycleResult, error) {
	result := LifecycleResult{RunID: prepared.RunID, State: prepared.State}
	if definition == nil {
		return result, errors.New("operation definition is required")
	}
	if prepared.State != StateAwaitingConfirm {
		return result, fmt.Errorf("confirmed execution requires state %s, got %s", StateAwaitingConfirm, prepared.State)
	}
	if definition.Metadata().ID != prepared.Metadata.ID {
		return result, errors.New("prepared operation metadata does not match execution definition")
	}
	state := prepared.State
	sequence := prepared.JournalSequence
	if err := coordinator.advance(ctx, &state, &sequence, prepared.RunID, StateQueued, "confirmed operation queued", "confirmed"); err != nil {
		result.State = state
		return result, err
	}
	if err := ctx.Err(); err != nil {
		if journalErr := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateCanceledBeforeApply, "operation canceled before lock acquisition", "canceled_before_apply"); journalErr != nil {
			err = errors.Join(err, journalErr)
		}
		result.State = state
		return result, err
	}

	if err := coordinator.advance(ctx, &state, &sequence, prepared.RunID, StateAcquiringLock, "acquire exclusive operation resources", "acquire_lock"); err != nil {
		result.State = state
		return result, err
	}
	resources, err := lockResources(prepared.Discovery.Targets)
	if err != nil {
		return coordinator.blockBeforeApply(state, sequence, prepared.RunID, result, err)
	}
	lease, err := coordinator.locks.Acquire(ctx, LockRequest{OwnerID: prepared.RunID, Resources: resources, TTL: coordinator.lockTTL})
	if err != nil {
		return coordinator.blockBeforeApply(state, sequence, prepared.RunID, result, fmt.Errorf("acquire operation lock: %w", err))
	}
	if err := ValidateLeaseCoverage(lease, prepared.RunID, resources, coordinator.now()); err != nil {
		cause := fmt.Errorf("validate acquired operation lock: %w", err)
		if releaseErr := coordinator.locks.Release(context.Background(), lease); releaseErr != nil {
			cause = errors.Join(cause, fmt.Errorf("release invalid operation lock: %w", releaseErr))
		}
		return coordinator.blockBeforeApply(state, sequence, prepared.RunID, result, cause)
	}
	session := newLeaseSession(coordinator.locks, lease, coordinator.lockTTL, coordinator.now)
	session.start()

	if err := coordinator.advance(ctx, &state, &sequence, prepared.RunID, StateCreatingRestorePoint, "create and verify restore point", "restore_point"); err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, err, false)
	}
	point, err := coordinator.restore.Create(ctx, RestorePointRequest{OperationID: prepared.Metadata.ID, RunID: prepared.RunID, Targets: prepared.Discovery.Targets, Plan: prepared.Plan, Retention: coordinator.restoreRetention})
	if err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, fmt.Errorf("create restore point: %w", err), false)
	}
	result.RestorePoint = point
	verification, err := coordinator.restore.Verify(ctx, point)
	if err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, fmt.Errorf("verify restore point: %w", err), false)
	}
	if !verification.Passed {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, errors.New("restore point verification failed"), false)
	}
	point.Status = RestorePointVerified
	result.RestorePoint = point
	if err := ValidateRestorePoint(point, coordinator.now()); err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, fmt.Errorf("validate verified restore point: %w", err), false)
	}
	if err := session.Err(); err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, fmt.Errorf("operation lock renewal failed before apply: %w", err), true)
	}
	if err := ctx.Err(); err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, err, false)
	}

	if err := coordinator.advance(ctx, &state, &sequence, prepared.RunID, StateRunning, "staged apply started", "apply"); err != nil {
		return coordinator.finishPrewriteWithLease(session, state, sequence, prepared.RunID, result, err, true)
	}
	applyCtx, cancelApply := session.bind(ctx)
	apply, applyErr := definition.Apply(applyCtx, ApplyInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Impact: prepared.Impact, RestorePoint: point, Lease: session})
	cancelApply()
	result.Apply = apply
	if leaseErr := session.Err(); leaseErr != nil {
		return coordinator.interruptAfterWrite(session, state, sequence, prepared.RunID, result, fmt.Errorf("operation lock renewal failed after apply started: %w", leaseErr))
	}
	if applyErr != nil {
		return coordinator.rollback(session, state, sequence, prepared, definition, result, fmt.Errorf("apply failed: %w", applyErr))
	}

	if err := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateVerifying, "verify applied state", "verify"); err != nil {
		return coordinator.rollback(session, state, sequence, prepared, definition, result, err)
	}
	verifyCtx, cancelVerify := session.bind(ctx)
	verify, verifyErr := definition.Verify(verifyCtx, VerifyInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Apply: apply})
	cancelVerify()
	result.Verification = verify
	if leaseErr := session.Err(); leaseErr != nil {
		return coordinator.interruptAfterWrite(session, state, sequence, prepared.RunID, result, fmt.Errorf("operation lock renewal failed during verification: %w", leaseErr))
	}
	if verifyErr != nil {
		return coordinator.rollback(session, state, sequence, prepared, definition, result, fmt.Errorf("verify failed: %w", verifyErr))
	}
	if !verify.Passed {
		return coordinator.rollback(session, state, sequence, prepared, definition, result, errors.New("verification did not pass"))
	}
	if err := coordinator.releaseLease(session); err != nil {
		if journalErr := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateInterrupted, "verified apply completed but lock release failed", "release_lock_failed"); journalErr != nil {
			err = errors.Join(err, journalErr)
		}
		result.State = state
		return result, err
	}
	if err := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateSucceeded, "operation completed and verified", "complete"); err != nil {
		result.State = state
		return result, err
	}
	result.State = state
	return result, nil
}

func (coordinator *Coordinator) prepareFailure(ctx context.Context, prepared PreparedRun, cause error) (PreparedRun, error) {
	target := StateBlocked
	message := "operation preparation blocked"
	checkpoint := "prepare_blocked"
	if ctx.Err() != nil {
		target = StateInterrupted
		message = "operation preparation interrupted"
		checkpoint = "prepare_interrupted"
	}
	if CanTransition(prepared.State, target) {
		if err := coordinator.advance(context.Background(), &prepared.State, &prepared.JournalSequence, prepared.RunID, target, message, checkpoint); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	return prepared, cause
}

func (coordinator *Coordinator) blockBeforeApply(state State, sequence int64, runID string, result LifecycleResult, cause error) (LifecycleResult, error) {
	if CanTransition(state, StateBlocked) {
		if err := coordinator.advance(context.Background(), &state, &sequence, runID, StateBlocked, "operation blocked before apply", "blocked_before_apply"); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	result.State = state
	return result, cause
}

func (coordinator *Coordinator) finishPrewriteWithLease(session *leaseSession, state State, sequence int64, runID string, result LifecycleResult, cause error, interrupted bool) (LifecycleResult, error) {
	releaseErr := coordinator.releaseLease(session)
	if releaseErr != nil {
		interrupted = true
		cause = errors.Join(cause, releaseErr)
	}
	target := StateBlocked
	message := "operation blocked before apply"
	checkpoint := "blocked_before_apply"
	if !interrupted && errors.Is(cause, context.Canceled) {
		target = StateCanceledBeforeApply
		message = "operation canceled before apply"
		checkpoint = "canceled_before_apply"
	} else if interrupted {
		target = StateInterrupted
		message = "operation interrupted before apply"
		checkpoint = "interrupted_before_apply"
	}
	if CanTransition(state, target) {
		if err := coordinator.advance(context.Background(), &state, &sequence, runID, target, message, checkpoint); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	result.State = state
	return result, cause
}

func (coordinator *Coordinator) interruptAfterWrite(session *leaseSession, state State, sequence int64, runID string, result LifecycleResult, cause error) (LifecycleResult, error) {
	if err := coordinator.releaseLease(session); err != nil {
		cause = errors.Join(cause, err)
	}
	if CanTransition(state, StateInterrupted) {
		if err := coordinator.advance(context.Background(), &state, &sequence, runID, StateInterrupted, "lock ownership became uncertain after write started; automated writes stopped", "lock_lost_after_write"); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	result.State = state
	return result, cause
}

func (coordinator *Coordinator) rollback(session *leaseSession, state State, sequence int64, prepared PreparedRun, definition OperationDefinition, result LifecycleResult, cause error) (LifecycleResult, error) {
	if err := session.Err(); err != nil {
		return coordinator.interruptAfterWrite(session, state, sequence, prepared.RunID, result, errors.Join(cause, fmt.Errorf("rollback blocked because lock renewal failed: %w", err)))
	}
	if err := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateRollingBack, "automatic rollback started", "rollback"); err != nil {
		result.State = state
		return result, errors.Join(cause, err)
	}
	rollbackCtx, cancel := context.WithTimeout(context.Background(), coordinator.rollbackTimeout)
	defer cancel()
	rollback, rollbackErr := definition.Rollback(rollbackCtx, RollbackInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Apply: result.Apply, RestorePoint: result.RestorePoint, Lease: session})
	result.Rollback = rollback
	if rollbackErr != nil {
		return coordinator.rollbackFailed(session, state, sequence, prepared.RunID, result, errors.Join(cause, fmt.Errorf("rollback failed: %w", rollbackErr)))
	}
	pluginVerification, verifyErr := definition.VerifyRollback(rollbackCtx, VerifyRollbackInput{Runtime: prepared.Runtime, Plan: prepared.Plan, Rollback: rollback, RestorePoint: result.RestorePoint})
	result.RollbackVerification = pluginVerification
	if verifyErr != nil || !pluginVerification.Passed {
		if verifyErr == nil {
			verifyErr = errors.New("plugin rollback verification did not pass")
		}
		return coordinator.rollbackFailed(session, state, sequence, prepared.RunID, result, errors.Join(cause, verifyErr))
	}
	restoreVerification, restoreErr := coordinator.restore.VerifyRestored(rollbackCtx, result.RestorePoint, rollback)
	if restoreErr != nil || !restoreVerification.Passed {
		if restoreErr == nil {
			restoreErr = errors.New("restore-point rollback verification did not pass")
		}
		return coordinator.rollbackFailed(session, state, sequence, prepared.RunID, result, errors.Join(cause, restoreErr))
	}
	if err := coordinator.releaseLease(session); err != nil {
		if journalErr := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateInterrupted, "rollback verified but lock release failed", "rollback_release_failed"); journalErr != nil {
			err = errors.Join(err, journalErr)
		}
		result.State = state
		return result, errors.Join(cause, err)
	}
	if err := coordinator.advance(context.Background(), &state, &sequence, prepared.RunID, StateRolledBack, "automatic rollback completed and verified", "rollback_verified"); err != nil {
		result.State = state
		return result, errors.Join(cause, err)
	}
	result.State = state
	return result, cause
}

func (coordinator *Coordinator) rollbackFailed(session *leaseSession, state State, sequence int64, runID string, result LifecycleResult, cause error) (LifecycleResult, error) {
	releaseErr := coordinator.releaseLease(session)
	if releaseErr != nil {
		cause = errors.Join(cause, releaseErr)
	}
	if CanTransition(state, StateRollbackFailed) {
		if err := coordinator.advance(context.Background(), &state, &sequence, runID, StateRollbackFailed, "automatic rollback could not be verified", "rollback_failed"); err != nil {
			cause = errors.Join(cause, err)
		}
	}
	result.State = state
	return result, cause
}

func (coordinator *Coordinator) releaseLease(session *leaseSession) error {
	session.stop()
	lease := session.Current()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := coordinator.locks.Release(ctx, lease); err != nil {
		return fmt.Errorf("release operation lock: %w", err)
	}
	return nil
}

func (coordinator *Coordinator) advance(ctx context.Context, state *State, sequence *int64, runID string, to State, message, checkpoint string) error {
	if err := Transition(*state, to); err != nil {
		return err
	}
	next := *sequence + 1
	entry := JournalEntry{RunID: runID, Sequence: next, State: to, Checkpoint: checkpoint, Message: message, At: coordinator.now()}
	if err := coordinator.journal.Append(ctx, entry); err != nil {
		return fmt.Errorf("append operation journal: %w", err)
	}
	*state = to
	*sequence = next
	return nil
}

func lockResources(targets []Target) ([]LockResource, error) {
	if len(targets) == 0 {
		return nil, errors.New("operation requires at least one discovered target")
	}
	resources := make([]LockResource, 0, len(targets))
	for _, target := range targets {
		key, err := ResourceLockKey(target)
		if err != nil {
			return nil, err
		}
		resources = append(resources, LockResource{Key: key})
	}
	normalized, err := NormalizeLockRequest(LockRequest{OwnerID: "validate", Resources: resources, TTL: time.Second})
	if err != nil {
		return nil, err
	}
	return normalized.Resources, nil
}

type leaseSession struct {
	manager LockManager
	ttl     time.Duration
	now     func() time.Time

	mu       sync.RWMutex
	lease    LockLease
	err      error
	stopCh   chan struct{}
	doneCh   chan struct{}
	lostCh   chan struct{}
	stopOnce sync.Once
	lostOnce sync.Once
}

func newLeaseSession(manager LockManager, lease LockLease, ttl time.Duration, now func() time.Time) *leaseSession {
	return &leaseSession{manager: manager, lease: lease, ttl: ttl, now: now, stopCh: make(chan struct{}), doneCh: make(chan struct{}), lostCh: make(chan struct{})}
}

func (session *leaseSession) start() {
	go session.renewLoop()
}

func (session *leaseSession) renewLoop() {
	defer close(session.doneCh)
	interval := session.ttl / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-session.stopCh:
			return
		case <-ticker.C:
			current := session.Current()
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			renewed, err := session.manager.Renew(ctx, current, session.ttl)
			cancel()
			if err != nil {
				session.mu.Lock()
				session.err = err
				session.mu.Unlock()
				session.lostOnce.Do(func() { close(session.lostCh) })
				return
			}
			session.mu.Lock()
			session.lease = renewed
			session.mu.Unlock()
		}
	}
}

func (session *leaseSession) Current() LockLease {
	session.mu.RLock()
	defer session.mu.RUnlock()
	lease := session.lease
	lease.Resources = append([]LockResource(nil), session.lease.Resources...)
	return lease
}

func (session *leaseSession) Validate(now time.Time) error {
	return ValidateLease(session.Current(), now)
}

func (session *leaseSession) Err() error {
	session.mu.RLock()
	defer session.mu.RUnlock()
	return session.err
}

func (session *leaseSession) bind(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-session.lostCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (session *leaseSession) stop() {
	session.stopOnce.Do(func() { close(session.stopCh) })
	<-session.doneCh
}

var _ LeaseHandle = (*leaseSession)(nil)
