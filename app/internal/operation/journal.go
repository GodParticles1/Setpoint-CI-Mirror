package operation

import (
	"context"
	"errors"
	"strings"
	"time"
)

type JournalEntry struct {
	RunID      string        `json:"run_id"`
	Sequence   int64         `json:"sequence"`
	State      State         `json:"state"`
	Checkpoint string        `json:"checkpoint,omitempty"`
	Message    string        `json:"message"`
	At         time.Time     `json:"at"`
	Evidence   []EvidenceRef `json:"evidence,omitempty"`
}

type Journal interface {
	Append(context.Context, JournalEntry) error
	List(context.Context, string) ([]JournalEntry, error)
}

func ValidateJournalEntry(entry JournalEntry) error {
	if strings.TrimSpace(entry.RunID) == "" {
		return errors.New("journal run ID is required")
	}
	if entry.Sequence < 1 {
		return errors.New("journal sequence must be positive")
	}
	if !ValidState(entry.State) {
		return errors.New("journal state is invalid")
	}
	if strings.TrimSpace(entry.Message) == "" {
		return errors.New("journal message is required")
	}
	if entry.At.IsZero() {
		return errors.New("journal timestamp is required")
	}
	return nil
}
