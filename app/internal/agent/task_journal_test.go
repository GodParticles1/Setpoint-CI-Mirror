package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"setpoint/internal/task"
)

func TestTaskJournalRoundTripReplacementAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "task-journal.json")
	journal, err := NewTaskJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := testJournalEntry(journalClaimed)
	if err := journal.Save(entry); err != nil {
		t.Fatal(err)
	}
	entry.State = journalExecuting
	if err := journal.Save(entry); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := journal.Load()
	if err != nil || !found || loaded.State != journalExecuting || loaded.Task.Metadata.ID != entry.Task.Metadata.ID {
		t.Fatalf("loaded=%#v found=%v err=%v", loaded, found, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("journal mode=%v", info.Mode().Perm())
		}
	}
	if err := journal.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := journal.Load(); err != nil || found {
		t.Fatalf("cleared journal found=%v err=%v", found, err)
	}
}

func TestTaskJournalRejectsCorruptOrUnsafeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-journal.json")
	journal, err := NewTaskJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Load(); err == nil {
		t.Fatal("unknown journal fields were accepted")
	}
	entry := testJournalEntry(journalCompleted)
	if err := journal.Save(entry); err == nil {
		t.Fatal("completed journal without submission was accepted")
	}
}

func testJournalEntry(state journalState) taskJournalEntry {
	now := time.Now().UTC()
	return taskJournalEntry{
		Version: 1, State: state,
		Task: task.Resource{
			APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
			Metadata: task.Metadata{ID: "task-1"},
			Spec:     task.Spec{NodeID: "agent-1", PluginID: "plugin-1"},
			Status:   task.Status{Phase: task.PhaseClaimed, ClaimID: "claim-1", UpdatedAt: now},
		},
	}
}
