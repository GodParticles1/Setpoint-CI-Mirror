package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type LedgerState string

const (
	LedgerPlanned            LedgerState = "planned"
	LedgerStaging            LedgerState = "staging"
	LedgerTransferred        LedgerState = "transferred"
	LedgerVerified           LedgerState = "verified"
	LedgerCommitPending      LedgerState = "commit_pending"
	LedgerReplicasConverging LedgerState = "replicas_converging"
	LedgerCommitUnknown      LedgerState = "commit_unknown"
	LedgerCommitted          LedgerState = "committed"
	LedgerFailed             LedgerState = "failed"
	LedgerRollbackPending    LedgerState = "rollback_pending"
	LedgerRolledBack         LedgerState = "rolled_back"
	LedgerRollbackBlocked    LedgerState = "rollback_blocked"
	LedgerRollbackFailed     LedgerState = "rollback_failed"
)

type DataFingerprint struct {
	Rows      uint64 `json:"rows"`
	Bytes     uint64 `json:"bytes,omitempty"`
	HashSum64 string `json:"hash_sum64,omitempty"`
	HashXor64 string `json:"hash_xor64,omitempty"`
}

type LedgerKey struct {
	RunID     string `json:"run_id"`
	Database  string `json:"database"`
	Table     string `json:"table"`
	Partition string `json:"partition,omitempty"`
	Chunk     uint64 `json:"chunk"`
}

type LedgerEntry struct {
	Key          LedgerKey       `json:"key"`
	Strategy     StrategyID      `json:"strategy"`
	State        LedgerState     `json:"state"`
	Attempt      uint32          `json:"attempt"`
	Checkpoint   string          `json:"checkpoint,omitempty"`
	StagingTable string          `json:"staging_table,omitempty"`
	Source       DataFingerprint `json:"source"`
	Target       DataFingerprint `json:"target"`
	LastError    string          `json:"last_error,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type LedgerStore interface {
	Put(context.Context, LedgerEntry) error
	Get(context.Context, LedgerKey) (LedgerEntry, bool, error)
	ListRun(context.Context, string) ([]LedgerEntry, error)
}

var ledgerTransitions = map[LedgerState]map[LedgerState]struct{}{
	LedgerPlanned:            {LedgerStaging: {}, LedgerFailed: {}},
	LedgerStaging:            {LedgerTransferred: {}, LedgerFailed: {}, LedgerRollbackPending: {}},
	LedgerTransferred:        {LedgerVerified: {}, LedgerFailed: {}, LedgerRollbackPending: {}},
	LedgerVerified:           {LedgerCommitPending: {}, LedgerCommitted: {}, LedgerCommitUnknown: {}, LedgerRollbackPending: {}},
	LedgerCommitPending:      {LedgerReplicasConverging: {}, LedgerCommitted: {}, LedgerCommitUnknown: {}},
	LedgerReplicasConverging: {LedgerCommitted: {}, LedgerCommitUnknown: {}},
	LedgerCommitUnknown:      {LedgerVerified: {}, LedgerReplicasConverging: {}, LedgerCommitted: {}, LedgerRollbackPending: {}, LedgerRollbackBlocked: {}},
	LedgerCommitted:          {LedgerRollbackPending: {}, LedgerRollbackBlocked: {}},
	LedgerFailed:             {LedgerRollbackPending: {}},
	LedgerRollbackPending:    {LedgerCommitted: {}, LedgerRolledBack: {}, LedgerRollbackBlocked: {}, LedgerRollbackFailed: {}},
}

var knownLedgerStates = map[LedgerState]struct{}{
	LedgerPlanned: {}, LedgerStaging: {}, LedgerTransferred: {}, LedgerVerified: {},
	LedgerCommitPending: {}, LedgerReplicasConverging: {}, LedgerCommitUnknown: {}, LedgerCommitted: {},
	LedgerFailed: {}, LedgerRollbackPending: {}, LedgerRolledBack: {}, LedgerRollbackBlocked: {}, LedgerRollbackFailed: {},
}

func CanLedgerTransition(from, to LedgerState) bool {
	_, ok := ledgerTransitions[from][to]
	return ok
}

func ValidateLedgerTransition(from, to LedgerState) error {
	if !CanLedgerTransition(from, to) {
		return fmt.Errorf("invalid ClickHouse ledger transition: %s -> %s", from, to)
	}
	return nil
}

func ValidateLedgerEntry(entry LedgerEntry) error {
	if strings.TrimSpace(entry.Key.RunID) == "" {
		return errors.New("ledger run ID is required")
	}
	if !validIdentifier(entry.Key.Database) || !validIdentifier(entry.Key.Table) {
		return errors.New("ledger database and table must be simple identifiers")
	}
	if entry.Key.Chunk == 0 {
		return errors.New("ledger chunk sequence must start at 1")
	}
	if !knownStrategyID(entry.Strategy) {
		return fmt.Errorf("unsupported ClickHouse ledger strategy %q", entry.Strategy)
	}
	if _, ok := knownLedgerStates[entry.State]; !ok {
		return fmt.Errorf("unsupported ClickHouse ledger state %q", entry.State)
	}
	if entry.Attempt == 0 {
		return errors.New("ledger attempt must start at 1")
	}
	if entry.UpdatedAt.IsZero() {
		return errors.New("ledger update time is required")
	}
	return nil
}

func knownStrategyID(strategy StrategyID) bool {
	for _, descriptor := range StrategyCatalog() {
		if descriptor.ID == strategy {
			return true
		}
	}
	return false
}

func LedgerTerminal(state LedgerState) bool {
	switch state {
	case LedgerCommitted, LedgerRolledBack, LedgerRollbackBlocked, LedgerRollbackFailed:
		return true
	default:
		return false
	}
}

func validateLedgerOwnership(entry LedgerEntry, chunk TransferChunk) error {
	if entry.Key != ledgerKeyForChunk(chunk) {
		return errors.New("ledger identity does not match the requested transfer chunk")
	}
	if entry.Strategy != chunk.Strategy || entry.StagingTable != chunk.StagingTable {
		return errors.New("ledger strategy or staging ownership does not match the requested transfer chunk")
	}
	expectedStaging, err := BuildStagingTableName(chunk.RunID, chunk.TargetTable)
	if err != nil {
		return fmt.Errorf("derive run-owned staging name: %w", err)
	}
	if chunk.StagingTable != expectedStaging {
		return fmt.Errorf("staging table %q is not owned by run %q for target %s.%s", chunk.StagingTable, chunk.RunID, chunk.TargetDatabase, chunk.TargetTable)
	}
	if err := validateRecoveryCheckpointOwnership(entry, chunk); err != nil {
		return err
	}
	return nil
}

func validateRecoveryCheckpointOwnership(entry LedgerEntry, chunk TransferChunk) error {
	partitionScoped := strings.TrimSpace(chunk.Partition) != ""
	owned := func(values ...string) bool {
		for _, value := range values {
			if entry.Checkpoint == value {
				return true
			}
		}
		return false
	}

	switch entry.State {
	case LedgerCommitPending:
		if !partitionScoped || !owned("replace_pending") {
			return fmt.Errorf("commit_pending checkpoint %q is not owned by the replicated partition commit path", entry.Checkpoint)
		}
	case LedgerReplicasConverging:
		if !partitionScoped || !owned("replicas_converging") {
			return fmt.Errorf("replicas_converging checkpoint %q is not owned by the replicated partition commit path", entry.Checkpoint)
		}
	case LedgerCommitUnknown:
		if partitionScoped {
			if !owned("commit_unknown") {
				return fmt.Errorf("partition commit_unknown checkpoint %q is not owned by the replicated partition commit path", entry.Checkpoint)
			}
		} else if !owned("exchange_intent") {
			return fmt.Errorf("whole-table commit_unknown checkpoint %q is not owned by the Atomic EXCHANGE commit path", entry.Checkpoint)
		}
	case LedgerRollbackPending:
		if owned("staging_drop_pending") {
			return nil
		}
		if partitionScoped {
			if !owned("drop_partition_pending", "rollback_observation_pending", "rollback_replicas_converging") {
				return fmt.Errorf("partition rollback_pending checkpoint %q is not owned by the replicated partition rollback path", entry.Checkpoint)
			}
		} else if !owned("rollback_exchange_intent", "rollback_observation_pending") {
			return fmt.Errorf("whole-table rollback_pending checkpoint %q is not owned by the Atomic EXCHANGE rollback path", entry.Checkpoint)
		}
	}
	return nil
}
