package task

import "testing"

func TestTaskPhaseClassification(t *testing.T) {
	for _, phase := range []Phase{PhasePending, PhaseClaimed, PhaseRunning, PhaseCancelRequested, PhaseCanceled, PhaseSucceeded, PhaseFailed} {
		if !ValidPhase(phase) {
			t.Fatalf("phase %q was not valid", phase)
		}
	}
	for _, phase := range []Phase{PhaseCanceled, PhaseSucceeded, PhaseFailed} {
		if !Terminal(phase) || !ValidResultPhase(phase) {
			t.Fatalf("phase %q was not terminal result phase", phase)
		}
	}
	if ValidPhase("unknown") || Terminal(PhaseRunning) || ValidResultPhase(PhaseRunning) {
		t.Fatal("invalid phase classification")
	}
}

func TestNewIDIsUnique(t *testing.T) {
	left, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if left == right || left == "" || right == "" {
		t.Fatalf("task IDs are not independent: %q %q", left, right)
	}
}
