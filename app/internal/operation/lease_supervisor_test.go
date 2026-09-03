package operation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type supervisorLockManager struct {
	mu                     sync.Mutex
	lease                  LockLease
	acquire                int
	renew                  int
	release                int
	failRenew              bool
	now                    time.Time
	currentErr             error
	returnLeaseForAnyOwner bool
}

func (manager *supervisorLockManager) Acquire(_ context.Context, request LockRequest) (LockLease, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.acquire++
	if manager.lease.ID != "" {
		return manager.lease, nil
	}
	manager.lease = LockLease{ID: "lease-1", OwnerID: request.OwnerID, Resources: append([]LockResource(nil), request.Resources...), AcquiredAt: manager.now, ExpiresAt: manager.now.Add(request.TTL)}
	return manager.lease, nil
}

func (manager *supervisorLockManager) Renew(_ context.Context, lease LockLease, ttl time.Duration) (LockLease, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.renew++
	if manager.failRenew {
		return LockLease{}, errors.New("renew failed")
	}
	if lease.ID != manager.lease.ID || lease.OwnerID != manager.lease.OwnerID || !lease.ExpiresAt.Equal(manager.lease.ExpiresAt) {
		return LockLease{}, errors.New("stale lease")
	}
	manager.lease.ExpiresAt = manager.now.Add(ttl)
	return manager.lease, nil
}

func (manager *supervisorLockManager) Release(_ context.Context, lease LockLease) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.release++
	if lease.ID != manager.lease.ID || lease.OwnerID != manager.lease.OwnerID {
		return errors.New("wrong lease")
	}
	manager.lease = LockLease{}
	return nil
}

func (manager *supervisorLockManager) CurrentLeaseByOwner(_ context.Context, ownerID string) (LockLease, bool, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.currentErr != nil {
		return LockLease{}, false, manager.currentErr
	}
	if manager.lease.ID == "" || (!manager.returnLeaseForAnyOwner && manager.lease.OwnerID != ownerID) || !manager.now.Before(manager.lease.ExpiresAt) {
		return LockLease{}, false, nil
	}
	return manager.lease, true, nil
}

func (manager *supervisorLockManager) counts() (int, int, int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.acquire, manager.renew, manager.release
}

func (manager *supervisorLockManager) setLease(lease LockLease) {
	manager.mu.Lock()
	manager.lease = lease
	manager.mu.Unlock()
}

func testSupervisor(t *testing.T, manager *supervisorLockManager, ttl, interval time.Duration) *LeaseSupervisor {
	t.Helper()
	supervisor, err := newLeaseSupervisor(manager, manager, ttl, interval, func() time.Time { manager.mu.Lock(); defer manager.mu.Unlock(); return manager.now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(supervisor.Close)
	return supervisor
}

func testSupervisorTargets() []Target {
	return []Target{{Kind: TargetNode, SiteID: "site-1", NodeID: "node-1", Component: "clickhouse", Resource: "db.table"}}
}

func TestLeaseSupervisorAcquireBindsRunAndRejectsDuplicateOwner(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now}
	supervisor := testSupervisor(t, manager, time.Minute, 10*time.Second)
	lease, err := supervisor.Acquire(context.Background(), "run-1", testSupervisorTargets())
	if err != nil {
		t.Fatal(err)
	}
	if lease.OwnerID != "run-1" {
		t.Fatalf("owner = %q", lease.OwnerID)
	}
	if _, err := supervisor.Acquire(context.Background(), "run-1", testSupervisorTargets()); err != nil {
		t.Fatal(err)
	}
	acquire, _, _ := manager.counts()
	if acquire != 1 {
		t.Fatalf("Acquire calls = %d, want 1", acquire)
	}
}

func TestLeaseSupervisorRejectsOwnerAndResourceMismatch(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now, returnLeaseForAnyOwner: true}
	resources, err := supervisedLockResources("run-1", testSupervisorTargets(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manager.setLease(LockLease{ID: "lease-1", OwnerID: "other-run", Resources: resources, AcquiredAt: now, ExpiresAt: now.Add(time.Minute)})
	supervisor := testSupervisor(t, manager, time.Minute, 10*time.Second)
	if _, err := supervisor.Resume(context.Background(), "run-1", testSupervisorTargets()); !errors.Is(err, ErrLeaseAuthorityUnavailable) {
		t.Fatalf("owner mismatch resume error = %v", err)
	}

	manager.setLease(LockLease{ID: "lease-2", OwnerID: "run-1", Resources: []LockResource{{Key: "different"}}, AcquiredAt: now, ExpiresAt: now.Add(time.Minute)})
	if _, err := supervisor.Resume(context.Background(), "run-1", testSupervisorTargets()); err == nil {
		t.Fatal("expected resource mismatch rejection")
	}
}

func TestLeaseSupervisorRenewalAndRemoteValidation(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now}
	supervisor := testSupervisor(t, manager, 60*time.Millisecond, 10*time.Millisecond)
	if _, err := supervisor.Acquire(context.Background(), "run-1", testSupervisorTargets()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, renew, _ := manager.counts()
		if renew > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("renewal did not run")
		}
		time.Sleep(time.Millisecond)
	}
	lease, found, err := supervisor.CurrentLeaseByOwner(context.Background(), "run-1")
	if err != nil || !found || lease.OwnerID != "run-1" {
		t.Fatalf("remote validation = %#v, %v, %v", lease, found, err)
	}
	if _, found, err := supervisor.CurrentLeaseByOwner(context.Background(), "other-run"); found || !errors.Is(err, ErrLeaseAuthorityUnavailable) {
		t.Fatalf("cross-run validation = found %v err %v", found, err)
	}
}

func TestLeaseSupervisorRenewFailureFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now, failRenew: true}
	supervisor := testSupervisor(t, manager, 60*time.Millisecond, 10*time.Millisecond)
	if _, err := supervisor.Acquire(context.Background(), "run-1", testSupervisorTargets()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, _, err := supervisor.CurrentLeaseByOwner(context.Background(), "run-1")
		if errors.Is(err, ErrLeaseAuthorityUnavailable) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("authority never failed closed: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLeaseSupervisorClassifiesExpiredAuthorityAsAbsent(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now}
	resources, err := supervisedLockResources("run-1", testSupervisorTargets(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manager.setLease(LockLease{ID: "lease-1", OwnerID: "run-1", Resources: resources, AcquiredAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(-time.Minute)})
	supervisor := testSupervisor(t, manager, time.Minute, 10*time.Second)
	if _, err := supervisor.Resume(context.Background(), "run-1", testSupervisorTargets()); !errors.Is(err, ErrLeaseAuthoritativeAbsence) {
		t.Fatalf("expired authority resume error = %v", err)
	}
}

func TestLeaseSupervisorRestartResumesOnlyProvableExistingAuthority(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now}
	resources, err := supervisedLockResources("run-1", testSupervisorTargets(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	manager.setLease(LockLease{ID: "lease-before-restart", OwnerID: "run-1", Resources: resources, AcquiredAt: now, ExpiresAt: now.Add(time.Minute)})
	supervisor := testSupervisor(t, manager, time.Minute, 10*time.Second)
	lease, err := supervisor.Resume(context.Background(), "run-1", testSupervisorTargets())
	if err != nil || lease.ID != "lease-before-restart" {
		t.Fatalf("resume = %#v, %v", lease, err)
	}
	acquire, _, _ := manager.counts()
	if acquire != 0 {
		t.Fatalf("restart performed Acquire %d times", acquire)
	}
	_, renew, _ := manager.counts()
	if renew != 1 {
		t.Fatalf("restart synchronous Renew calls = %d, want 1", renew)
	}

	missing := &supervisorLockManager{now: now}
	missingSupervisor := testSupervisor(t, missing, time.Minute, 10*time.Second)
	if _, err := missingSupervisor.Resume(context.Background(), "run-1", testSupervisorTargets()); !errors.Is(err, ErrLeaseAuthoritativeAbsence) {
		t.Fatalf("missing authority resume error = %v", err)
	}
	acquire, _, _ = missing.counts()
	if acquire != 0 {
		t.Fatalf("missing authority performed Acquire %d times", acquire)
	}
}

func TestLeaseSupervisorResumeFailsClosedWithoutAcquireOnAuthorityOrRenewFailure(t *testing.T) {
	now := time.Now().UTC()
	resources, err := supervisedLockResources("run-1", testSupervisorTargets(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		manager *supervisorLockManager
	}{
		{name: "authority", manager: &supervisorLockManager{now: now, currentErr: errors.New("authority read failed")}},
		{name: "renew", manager: &supervisorLockManager{
			now: now, failRenew: true,
			lease: LockLease{ID: "lease-before-restart", OwnerID: "run-1", Resources: resources, AcquiredAt: now, ExpiresAt: now.Add(time.Minute)},
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			supervisor := testSupervisor(t, testCase.manager, time.Minute, 10*time.Second)
			if _, err := supervisor.Resume(context.Background(), "run-1", testSupervisorTargets()); !errors.Is(err, ErrLeaseAuthorityUnavailable) {
				t.Fatalf("Resume error = %v, want ErrLeaseAuthorityUnavailable", err)
			}
			acquire, _, _ := testCase.manager.counts()
			if acquire != 0 {
				t.Fatalf("Resume fell back to Acquire %d times", acquire)
			}
		})
	}
}

func TestLeaseSupervisorReleaseRequiresCurrentAuthority(t *testing.T) {
	now := time.Now().UTC()
	manager := &supervisorLockManager{now: now}
	supervisor := testSupervisor(t, manager, time.Minute, 10*time.Second)
	if _, err := supervisor.Acquire(context.Background(), "run-1", testSupervisorTargets()); err != nil {
		t.Fatal(err)
	}
	manager.currentErr = fmt.Errorf("authority read failed")
	if err := supervisor.Release(context.Background(), "run-1"); err == nil {
		t.Fatal("expected release to fail closed")
	}
	_, _, releases := manager.counts()
	if releases != 0 {
		t.Fatalf("Release calls = %d, want 0", releases)
	}
}
