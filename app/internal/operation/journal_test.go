package operation

import (
	"testing"
	"time"
)

func TestValidateJournalEntry(t *testing.T) {
	entry := JournalEntry{RunID: "run-1", Sequence: 1, State: StatePlanned, Message: "plan created", At: time.Now()}
	if err := ValidateJournalEntry(entry); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	entry.Sequence = 0
	if err := ValidateJournalEntry(entry); err == nil {
		t.Fatal("zero sequence accepted")
	}
}
