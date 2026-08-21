package operation

import "testing"

func TestStateMachineHappyPath(t *testing.T) {
	path := []State{
		StateDraft,
		StateDiscovering,
		StatePrechecking,
		StatePlanned,
		StateAwaitingConfirm,
		StateQueued,
		StateAcquiringLock,
		StateCreatingRestorePoint,
		StateRunning,
		StateVerifying,
		StateSucceeded,
	}
	for i := 0; i < len(path)-1; i++ {
		if err := Transition(path[i], path[i+1]); err != nil {
			t.Fatalf("transition %s -> %s: %v", path[i], path[i+1], err)
		}
	}
	if !Terminal(StateSucceeded) {
		t.Fatal("succeeded must be terminal")
	}
}

func TestStateMachineRejectsSkippedSafetyCheckpoints(t *testing.T) {
	if CanTransition(StateQueued, StateRunning) {
		t.Fatal("queued -> running must be rejected; lock and restore point checkpoints are mandatory")
	}
	if CanTransition(StateRunning, StateSucceeded) {
		t.Fatal("running -> succeeded must be rejected")
	}
}

func TestInterruptedRunRequiresResumeOrRollback(t *testing.T) {
	if Terminal(StateInterrupted) {
		t.Fatal("interrupted state must not be terminal")
	}
	if !CanTransition(StateInterrupted, StateQueued) || !CanTransition(StateInterrupted, StateRollingBack) {
		t.Fatal("interrupted state must support resume or rollback")
	}
}

func TestBlockedAndCanceledAreTerminalWithoutApply(t *testing.T) {
	if !Terminal(StateBlocked) || !Terminal(StateCanceledBeforeApply) {
		t.Fatal("blocked and canceled-before-apply must be terminal")
	}
}

func TestPreApplyStatesPermitSafeCancellation(t *testing.T) {
	for _, state := range []State{StateDraft, StateDiscovering, StatePrechecking, StatePlanned, StateAwaitingConfirm} {
		if !CanTransition(state, StateCanceledBeforeApply) {
			t.Fatalf("state %s must permit cancellation before Apply", state)
		}
	}
	for _, state := range []State{StateRunning, StateVerifying, StateRollingBack} {
		if CanTransition(state, StateCanceledBeforeApply) {
			t.Fatalf("state %s must not claim cancellation before Apply", state)
		}
	}
}
