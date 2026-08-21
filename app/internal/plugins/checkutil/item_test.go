package checkutil

import (
	"testing"
	"time"

	"setpoint/internal/task"
)

func TestItemsKeepFiveStatesConsistent(t *testing.T) {
	now := time.Now().UTC()
	definition := Definition{ID: "test.item", Name: "Test", Recommended: "secure", Risk: "low"}
	items := []task.CheckItem{
		Value(definition, "secure", true, "value read", now),
		Value(definition, "insecure", false, "value read", now),
		ManualReview(definition, "policy dependent", "target policy is not configured", "value read", now),
		Error(definition, "read_failed", "failed", "read failed", now),
		NotApplicable(definition, "component absent", now),
	}
	for index, item := range items {
		if err := task.ValidateItem(item); err != nil {
			t.Fatalf("item %d invalid: %v", index, err)
		}
	}
}

func TestManualReviewRequiresReason(t *testing.T) {
	definition := Definition{ID: "test.item", Name: "Test", Recommended: "secure", Risk: "low"}
	item := ManualReview(definition, "policy dependent", "", "value read", time.Now().UTC())
	if err := task.ValidateItem(item); err == nil {
		t.Fatal("manual review item without a reason was accepted")
	}
}
