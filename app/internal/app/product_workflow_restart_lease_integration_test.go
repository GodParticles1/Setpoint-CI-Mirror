package app

import (
	"testing"

	"setpoint/internal/operation"
)

func TestCanceledCreateRestorePointRestartReestablishesLeaseSupervision(t *testing.T) {
	fixture := newCanceledRestorePointRestartFixture(t)
	runID := fixture.run.Metadata.ID
	leaseBeforeRestart, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, runID)
	if err != nil || !found {
		t.Fatalf("persisted lease before restart found=%v err=%v", found, err)
	}
	fixture.supervisor.Close()

	supervisor2, err := operation.NewLeaseSupervisor(fixture.store, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(supervisor2.Close)
	product2, err := NewProductOperations(fixture.base, fixture.store, supervisor2, fixture.execution)
	if err != nil {
		t.Fatal(err)
	}
	if err := product2.ResumeOperationRuns(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	resumed, found, err := supervisor2.CurrentLeaseByOwner(fixture.ctx, runID)
	if err != nil || !found || resumed.ID != leaseBeforeRestart.ID || !resumed.ExpiresAt.After(leaseBeforeRestart.ExpiresAt) {
		t.Fatalf("restarted supervision lease=%#v found=%v err=%v before=%#v", resumed, found, err, leaseBeforeRestart)
	}
	if _, err := fixture.store.Acquire(fixture.ctx, lockRequestForRun(t, fixture.run, "competing-run")); err == nil {
		t.Fatal("competing run acquired target while canceled restore-point task was unresolved")
	}
}
