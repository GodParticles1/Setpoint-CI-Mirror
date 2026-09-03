package operation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

var (
	ErrLeaseAuthorityUnavailable = errors.New("server operation lease authority is unavailable")
	ErrLeaseAuthoritativeAbsence = errors.New("server operation lease is authoritatively absent")
)

type CurrentLeaseReader interface {
	CurrentLeaseByOwner(context.Context, string) (LockLease, bool, error)
}

type LeaseSupervisor struct {
	locks      LockManager
	authority  CurrentLeaseReader
	ttl        time.Duration
	renewEvery time.Duration
	now        func() time.Time

	mu     sync.Mutex
	active map[string]*supervisedLease
	closed bool
}

type supervisedLease struct {
	lease     LockLease
	resources []LockResource
	cancel    context.CancelFunc
	done      chan struct{}
	failure   error
	stopping  bool
}

func NewLeaseSupervisor(locks LockManager, authority CurrentLeaseReader) (*LeaseSupervisor, error) {
	return newLeaseSupervisor(locks, authority, defaultOperationLockTTL, defaultOperationLockTTL/3, func() time.Time { return time.Now().UTC() })
}

func newLeaseSupervisor(locks LockManager, authority CurrentLeaseReader, ttl, renewEvery time.Duration, now func() time.Time) (*LeaseSupervisor, error) {
	if locks == nil || authority == nil {
		return nil, errors.New("lock manager and current lease authority are required")
	}
	if ttl <= 0 || renewEvery <= 0 || renewEvery >= ttl {
		return nil, errors.New("lease TTL and renewal interval are invalid")
	}
	if now == nil {
		return nil, errors.New("lease supervisor clock is required")
	}
	return &LeaseSupervisor{locks: locks, authority: authority, ttl: ttl, renewEvery: renewEvery, now: now, active: map[string]*supervisedLease{}}, nil
}

func (supervisor *LeaseSupervisor) Acquire(ctx context.Context, runID string, targets []Target) (LockLease, error) {
	runID = strings.TrimSpace(runID)
	resources, err := supervisedLockResources(runID, targets, supervisor.ttl)
	if err != nil {
		return LockLease{}, err
	}

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return LockLease{}, ErrLeaseAuthorityUnavailable
	}
	if existing, ok := supervisor.active[runID]; ok {
		if existing.failure != nil {
			return LockLease{}, fmt.Errorf("%w: %v", ErrLeaseAuthorityUnavailable, existing.failure)
		}
		if !reflect.DeepEqual(existing.resources, resources) {
			return LockLease{}, errors.New("operation run already has a different supervised lease resource set")
		}
		if err := validateExactLeaseBinding(existing.lease, runID, resources, supervisor.now()); err != nil {
			return LockLease{}, err
		}
		return existing.lease, nil
	}

	lease, err := supervisor.locks.Acquire(ctx, LockRequest{OwnerID: runID, Resources: resources, TTL: supervisor.ttl})
	if err != nil {
		return LockLease{}, fmt.Errorf("acquire authoritative operation lease: %w", err)
	}
	if err := validateExactLeaseBinding(lease, runID, resources, supervisor.now()); err != nil {
		return LockLease{}, fmt.Errorf("validate acquired authoritative operation lease: %w", err)
	}
	supervisor.startLocked(runID, lease, resources)
	return lease, nil
}

func (supervisor *LeaseSupervisor) Resume(ctx context.Context, runID string, targets []Target) (LockLease, error) {
	runID = strings.TrimSpace(runID)
	resources, err := supervisedLockResources(runID, targets, supervisor.ttl)
	if err != nil {
		return LockLease{}, err
	}

	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return LockLease{}, ErrLeaseAuthorityUnavailable
	}
	if existing, ok := supervisor.active[runID]; ok {
		if existing.failure != nil {
			return LockLease{}, fmt.Errorf("%w: %v", ErrLeaseAuthorityUnavailable, existing.failure)
		}
		if !reflect.DeepEqual(existing.resources, resources) {
			return LockLease{}, fmt.Errorf("%w: operation run already has a different supervised lease resource set", ErrLeaseAuthorityUnavailable)
		}
		if err := validateExactLeaseBinding(existing.lease, runID, resources, supervisor.now()); err != nil {
			return LockLease{}, fmt.Errorf("%w: validate supervised operation lease: %v", ErrLeaseAuthorityUnavailable, err)
		}
		return existing.lease, nil
	}

	lease, found, err := supervisor.authority.CurrentLeaseByOwner(ctx, runID)
	if err != nil {
		return LockLease{}, fmt.Errorf("%w: inspect current authoritative operation lease: %v", ErrLeaseAuthorityUnavailable, err)
	}
	if !found {
		return LockLease{}, ErrLeaseAuthoritativeAbsence
	}
	if err := validateExactLeaseBinding(lease, runID, resources, supervisor.now()); err != nil {
		return LockLease{}, fmt.Errorf("%w: reconcile current authoritative operation lease: %v", ErrLeaseAuthorityUnavailable, err)
	}
	renewed, err := supervisor.locks.Renew(ctx, lease, supervisor.ttl)
	if err != nil {
		return LockLease{}, fmt.Errorf("%w: synchronously renew resumed operation lease: %v", ErrLeaseAuthorityUnavailable, err)
	}
	if err := validateExactLeaseBinding(renewed, runID, resources, supervisor.now()); err != nil {
		return LockLease{}, fmt.Errorf("%w: validate synchronously renewed operation lease: %v", ErrLeaseAuthorityUnavailable, err)
	}
	supervisor.startLocked(runID, renewed, resources)
	return renewed, nil
}

func (supervisor *LeaseSupervisor) CurrentLeaseByOwner(ctx context.Context, runID string) (LockLease, bool, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return LockLease{}, false, errors.New("operation run ID is required")
	}

	supervisor.mu.Lock()
	current, ok := supervisor.active[runID]
	if !ok || supervisor.closed {
		supervisor.mu.Unlock()
		return LockLease{}, false, ErrLeaseAuthorityUnavailable
	}
	if current.failure != nil {
		err := current.failure
		supervisor.mu.Unlock()
		return LockLease{}, false, fmt.Errorf("%w: %v", ErrLeaseAuthorityUnavailable, err)
	}
	expectedID := current.lease.ID
	expectedAcquiredAt := current.lease.AcquiredAt
	resources := append([]LockResource(nil), current.resources...)
	supervisor.mu.Unlock()

	lease, found, err := supervisor.authority.CurrentLeaseByOwner(ctx, runID)
	if err != nil {
		supervisor.fail(runID, fmt.Errorf("inspect current authoritative lease: %w", err))
		return LockLease{}, false, err
	}
	if !found {
		err := ErrLeaseAuthorityUnavailable
		supervisor.fail(runID, err)
		return LockLease{}, false, err
	}
	if lease.ID != expectedID || !lease.AcquiredAt.Equal(expectedAcquiredAt) {
		err := errors.New("authoritative lease identity changed while operation run was supervised")
		supervisor.fail(runID, err)
		return LockLease{}, false, err
	}
	if err := validateExactLeaseBinding(lease, runID, resources, supervisor.now()); err != nil {
		supervisor.fail(runID, err)
		return LockLease{}, false, err
	}

	supervisor.mu.Lock()
	if current, ok := supervisor.active[runID]; ok && current.failure == nil && current.lease.ID == lease.ID {
		current.lease = lease
	}
	supervisor.mu.Unlock()
	return lease, true, nil
}

func (supervisor *LeaseSupervisor) Release(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return errors.New("operation run ID is required")
	}

	supervisor.mu.Lock()
	current, ok := supervisor.active[runID]
	if !ok {
		supervisor.mu.Unlock()
		return ErrLeaseAuthorityUnavailable
	}
	if current.failure != nil {
		err := current.failure
		supervisor.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrLeaseAuthorityUnavailable, err)
	}
	current.stopping = true
	current.cancel()
	done := current.done
	resources := append([]LockResource(nil), current.resources...)
	expectedID := current.lease.ID
	expectedAcquiredAt := current.lease.AcquiredAt
	supervisor.mu.Unlock()
	<-done

	lease, found, err := supervisor.authority.CurrentLeaseByOwner(ctx, runID)
	if err != nil {
		supervisor.fail(runID, fmt.Errorf("inspect authoritative lease before release: %w", err))
		return err
	}
	if !found || lease.ID != expectedID || !lease.AcquiredAt.Equal(expectedAcquiredAt) {
		err := ErrLeaseAuthorityUnavailable
		supervisor.fail(runID, err)
		return err
	}
	if err := validateExactLeaseBinding(lease, runID, resources, supervisor.now()); err != nil {
		supervisor.fail(runID, err)
		return err
	}
	if err := supervisor.locks.Release(ctx, lease); err != nil {
		supervisor.fail(runID, fmt.Errorf("release authoritative operation lease: %w", err))
		return err
	}

	supervisor.mu.Lock()
	delete(supervisor.active, runID)
	supervisor.mu.Unlock()
	return nil
}

func (supervisor *LeaseSupervisor) Close() {
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		return
	}
	supervisor.closed = true
	var done []chan struct{}
	for _, current := range supervisor.active {
		current.stopping = true
		current.cancel()
		done = append(done, current.done)
	}
	supervisor.mu.Unlock()
	for _, channel := range done {
		<-channel
	}
}

func (supervisor *LeaseSupervisor) startLocked(runID string, lease LockLease, resources []LockResource) {
	ctx, cancel := context.WithCancel(context.Background())
	current := &supervisedLease{lease: lease, resources: append([]LockResource(nil), resources...), cancel: cancel, done: make(chan struct{})}
	supervisor.active[runID] = current
	go supervisor.renewLoop(ctx, runID, current)
}

func (supervisor *LeaseSupervisor) renewLoop(ctx context.Context, runID string, tracked *supervisedLease) {
	ticker := time.NewTicker(supervisor.renewEvery)
	defer ticker.Stop()
	defer close(tracked.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			supervisor.mu.Lock()
			current, ok := supervisor.active[runID]
			if !ok || current != tracked || current.stopping || current.failure != nil {
				supervisor.mu.Unlock()
				return
			}
			lease := current.lease
			resources := append([]LockResource(nil), current.resources...)
			supervisor.mu.Unlock()

			renewed, err := supervisor.locks.Renew(ctx, lease, supervisor.ttl)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				supervisor.fail(runID, fmt.Errorf("renew authoritative operation lease: %w", err))
				return
			}
			if err := validateExactLeaseBinding(renewed, runID, resources, supervisor.now()); err != nil {
				supervisor.fail(runID, fmt.Errorf("validate renewed authoritative operation lease: %w", err))
				return
			}

			supervisor.mu.Lock()
			if current, ok := supervisor.active[runID]; ok && current == tracked && !current.stopping && current.failure == nil {
				current.lease = renewed
			}
			supervisor.mu.Unlock()
		}
	}
}

func (supervisor *LeaseSupervisor) fail(runID string, err error) {
	if err == nil {
		return
	}
	supervisor.mu.Lock()
	if current, ok := supervisor.active[runID]; ok && current.failure == nil {
		current.failure = err
		if !current.stopping {
			current.cancel()
		}
	}
	supervisor.mu.Unlock()
}

func supervisedLockResources(runID string, targets []Target, ttl time.Duration) ([]LockResource, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("operation run ID is required")
	}
	if len(targets) == 0 {
		return nil, errors.New("operation lease requires at least one frozen target")
	}
	resources := make([]LockResource, 0, len(targets))
	for _, target := range targets {
		key, err := ResourceLockKey(target)
		if err != nil {
			return nil, fmt.Errorf("operation lease target: %w", err)
		}
		resources = append(resources, LockResource{Key: key})
	}
	normalized, err := NormalizeLockRequest(LockRequest{OwnerID: runID, Resources: resources, TTL: ttl})
	if err != nil {
		return nil, err
	}
	return normalized.Resources, nil
}

func validateExactLeaseBinding(lease LockLease, runID string, resources []LockResource, now time.Time) error {
	if err := ValidateLeaseCoverage(lease, runID, resources, now); err != nil {
		return err
	}
	normalized, err := NormalizeLockRequest(LockRequest{OwnerID: runID, Resources: lease.Resources, TTL: time.Second})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalized.Resources, resources) {
		return errors.New("operation lease resource coverage differs from the frozen target resource set")
	}
	return nil
}
