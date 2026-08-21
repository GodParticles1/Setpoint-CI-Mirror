package checkrun

import (
	"testing"
	"time"

	"setpoint/internal/task"
)

func TestAggregateKeepsManualReviewSeparate(t *testing.T) {
	now := time.Now().UTC()
	items := []task.CheckItem{
		{Status: task.ItemSafe},
		{Status: task.ItemUnsafe},
		{Status: task.ItemManualReview},
		{Status: task.ItemError},
		{Status: task.ItemNotApplicable},
	}
	status := Aggregate([]task.Resource{{
		Status: task.Status{Phase: task.PhaseSucceeded, UpdatedAt: now},
		Result: &task.CheckResult{Items: items},
	}})
	if status.Phase != PhaseCompleted || status.Counts.Safe != 1 || status.Counts.Unsafe != 1 ||
		status.Counts.ManualReview != 1 || status.Counts.Error != 1 || status.Counts.NotApplicable != 1 {
		t.Fatalf("aggregate status = %#v", status)
	}
}
