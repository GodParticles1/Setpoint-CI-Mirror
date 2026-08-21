package clickhouse

type StrategyID string

const (
	StrategyNativeStream         StrategyID = "native_stream"
	StrategyBuiltinBackupRestore StrategyID = "builtin_backup_restore"
	StrategyRemoteTableFunction  StrategyID = "remote_table_function"
)

type StrategyDescriptor struct {
	ID                   StrategyID `json:"id"`
	SupportsTimeRange    bool       `json:"supports_time_range"`
	SupportsWholeTable   bool       `json:"supports_whole_table"`
	RequiresRemoteAccess bool       `json:"requires_remote_access"`
	RequiresBackupAPI    bool       `json:"requires_backup_api"`
	PlanningReady        bool       `json:"planning_ready"`
	ApplyReady           bool       `json:"apply_ready"`
}

func StrategyCatalog() []StrategyDescriptor {
	return []StrategyDescriptor{
		{ID: StrategyNativeStream, SupportsTimeRange: true, SupportsWholeTable: true, PlanningReady: true, ApplyReady: false},
		{ID: StrategyBuiltinBackupRestore, SupportsWholeTable: true, RequiresBackupAPI: true, PlanningReady: true, ApplyReady: false},
		{ID: StrategyRemoteTableFunction, SupportsTimeRange: true, SupportsWholeTable: true, RequiresRemoteAccess: true, PlanningReady: false, ApplyReady: false},
	}
}

type StrategyDecision struct {
	Selected                 StrategyID   `json:"selected,omitempty"`
	Candidates               []StrategyID `json:"candidates,omitempty"`
	Reason                   string       `json:"reason"`
	RequiresCapabilityChecks []string     `json:"requires_capability_checks,omitempty"`
}

type StrategySelector struct{}

func NewStrategySelector() *StrategySelector { return &StrategySelector{} }

func (selector *StrategySelector) Select(parameters Parameters, source, target Snapshot, precheck PrecheckReport) StrategyDecision {
	if !precheck.Compatible {
		return StrategyDecision{Reason: "precheck has blocking findings"}
	}

	decision := StrategyDecision{Selected: StrategyNativeStream, Candidates: []StrategyID{StrategyNativeStream}}
	if parameters.StartTime != "" || parameters.EndTime != "" {
		decision.Reason = "time-bounded migration requires staged streaming and reconciliation; Native stream is the V1 planning strategy"
		return decision
	}

	decision.Candidates = append(decision.Candidates, StrategyBuiltinBackupRestore)
	decision.RequiresCapabilityChecks = append(decision.RequiresCapabilityChecks, "clickhouse_backup_restore_support")
	decision.Reason = "whole-table migration can use Native stream; built-in BACKUP/RESTORE remains a candidate pending runtime capability discovery"
	return decision
}
