package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"setpoint/internal/task"
)

type journalState string

const (
	journalClaimed   journalState = "claimed"
	journalExecuting journalState = "executing"
	journalCompleted journalState = "completed"
)

type taskJournalEntry struct {
	Version    int                    `json:"version"`
	State      journalState           `json:"state"`
	Task       task.Resource          `json:"task"`
	Submission *task.ResultSubmission `json:"submission,omitempty"`
}

type TaskJournal struct {
	path string
}

func NewTaskJournal(path string) (*TaskJournal, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("task journal path is required")
	}
	return &TaskJournal{path: filepath.Clean(path)}, nil
}

func (journal *TaskJournal) Load() (taskJournalEntry, bool, error) {
	contents, err := os.ReadFile(journal.path)
	if errors.Is(err, os.ErrNotExist) {
		return taskJournalEntry{}, false, nil
	}
	if err != nil {
		return taskJournalEntry{}, false, fmt.Errorf("read task journal: %w", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(journal.path)
		if err != nil {
			return taskJournalEntry{}, false, fmt.Errorf("inspect task journal permissions: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return taskJournalEntry{}, false, fmt.Errorf("task journal permissions %04o expose execution data", info.Mode().Perm())
		}
	}
	var entry taskJournalEntry
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entry); err != nil {
		return taskJournalEntry{}, false, fmt.Errorf("decode task journal: %w", err)
	}
	if err := validateJournalEntry(entry); err != nil {
		return taskJournalEntry{}, false, err
	}
	entry.Task = task.Clone(entry.Task)
	return entry, true, nil
}

func (journal *TaskJournal) Save(entry taskJournalEntry) error {
	if err := validateJournalEntry(entry); err != nil {
		return err
	}
	contents, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode task journal: %w", err)
	}
	contents = append(contents, '\n')
	directory := filepath.Dir(journal.path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create task journal directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".task-journal-*")
	if err != nil {
		return fmt.Errorf("create temporary task journal: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict task journal permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write task journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync task journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close task journal: %w", err)
	}
	if err := replaceJournalFile(temporaryPath, journal.path); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync task journal directory: %w", err)
	}
	return nil
}

func (journal *TaskJournal) Clear() error {
	if err := os.Remove(journal.path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("remove acknowledged task journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(journal.path)); err != nil {
		return fmt.Errorf("sync cleared task journal directory: %w", err)
	}
	return nil
}

func validateJournalEntry(entry taskJournalEntry) error {
	if entry.Version != 1 {
		return fmt.Errorf("unsupported task journal version %d", entry.Version)
	}
	if entry.Task.Metadata.ID == "" || entry.Task.Status.ClaimID == "" {
		return errors.New("task journal is missing task or claim identity")
	}
	switch entry.Task.Kind {
	case "", task.KindReadOnlyCheckTask:
		if entry.Task.Spec.PluginID == "" || entry.Task.Spec.OperationID != "" {
			return errors.New("check task journal has an invalid execution identity")
		}
		if entry.Task.Spec.Execution != nil {
			if err := task.ValidateCheckExecutionContract(*entry.Task.Spec.Execution, entry.Task.Spec.ContractDigest); err != nil {
				return fmt.Errorf("check task journal contract: %w", err)
			}
		}
	case task.KindOperationPlanningTask:
		if entry.Task.Spec.PluginID != "" || entry.Task.Spec.Execution != nil || entry.Task.Spec.ContractDigest != "" ||
			entry.Task.Spec.OperationID == "" || entry.Task.Spec.OperationVersion == "" || entry.Task.Spec.CapabilityDigest == "" {
			return errors.New("operation task journal is missing frozen operation identity")
		}
	case task.KindOperationExecutionTask:
		if entry.Task.Spec.PluginID != "" || entry.Task.Spec.Execution != nil || entry.Task.Spec.OperationExecution == nil ||
			entry.Task.Spec.OperationID == "" || entry.Task.Spec.OperationVersion == "" || entry.Task.Spec.CapabilityDigest == "" {
			return errors.New("operation execution journal is missing frozen bounded-action identity")
		}
		if err := task.ValidateOperationExecutionContract(*entry.Task.Spec.OperationExecution, entry.Task.Spec.ContractDigest); err != nil {
			return fmt.Errorf("operation execution journal contract: %w", err)
		}
	default:
		return fmt.Errorf("unsupported task journal kind %q", entry.Task.Kind)
	}
	switch entry.State {
	case journalClaimed, journalExecuting:
		if entry.Submission != nil {
			return errors.New("unfinished task journal must not contain a submission")
		}
	case journalCompleted:
		if entry.Submission == nil || entry.Submission.ClaimID != entry.Task.Status.ClaimID {
			return errors.New("completed task journal requires a matching submission")
		}
	default:
		return fmt.Errorf("unsupported task journal state %q", entry.State)
	}
	return nil
}

func replaceJournalFile(temporaryPath, targetPath string) error {
	if err := os.Rename(temporaryPath, targetPath); err == nil {
		return nil
	} else if _, statErr := os.Stat(targetPath); statErr != nil {
		return fmt.Errorf("install task journal: %w", err)
	}
	backupPath := targetPath + ".previous"
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale task journal backup: %w", err)
	}
	if err := os.Rename(targetPath, backupPath); err != nil {
		return fmt.Errorf("stage existing task journal: %w", err)
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		if restoreErr := os.Rename(backupPath, targetPath); restoreErr != nil {
			return fmt.Errorf("install task journal: %v; restore previous journal: %w", err, restoreErr)
		}
		return fmt.Errorf("install task journal: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("remove previous task journal: %w", err)
	}
	return nil
}
