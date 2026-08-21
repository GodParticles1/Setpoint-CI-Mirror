package clickhouse

import "strings"

type StrategyCompatibilityDecision struct {
	Selected   StrategyID   `json:"selected,omitempty"`
	Candidates []StrategyID `json:"candidates,omitempty"`
	Reason     string       `json:"reason"`
	Fallback   StrategyID   `json:"fallback,omitempty"`
}

// SelectCompatible chooses the most efficient proven strategy and keeps a
// conservative fallback. It never enables a feature from version text alone;
// runtime capability evidence must mark the feature supported.
func (selector *StrategySelector) SelectCompatible(parameters Parameters, source, target Snapshot, precheck PrecheckReport, capabilities CapabilityIntersection) StrategyCompatibilityDecision {
	if !precheck.Compatible {
		return StrategyCompatibilityDecision{Reason: "precheck has blocking findings"}
	}

	hasTimeRange := strings.TrimSpace(parameters.StartTime) != "" || strings.TrimSpace(parameters.EndTime) != ""
	decision := StrategyCompatibilityDecision{Fallback: StrategyNativeStream}

	if hasTimeRange {
		if capabilities.BothSupport(CapabilityNativeFormat) {
			decision.Selected = StrategyNativeStream
			decision.Candidates = []StrategyID{StrategyNativeStream}
			decision.Reason = "time-bounded migration uses proven Native-format streaming; whole-table-only strategies are not considered"
			return decision
		}
		decision.Reason = "time-bounded migration has no proven compatible transport capability"
		return decision
	}

	// Built-in BACKUP/RESTORE can be more efficient for a compatible whole-table
	// migration because ClickHouse owns the storage-level transfer semantics. It
	// is preferred only after both endpoints prove support and the dedicated
	// backup/restore safety precheck approves the concrete topology.
	if capabilities.BothSupport(CapabilityBuiltinBackupRestore) {
		decision.Selected = StrategyBuiltinBackupRestore
		decision.Candidates = []StrategyID{StrategyBuiltinBackupRestore}
		if capabilities.BothSupport(CapabilityNativeFormat) {
			decision.Candidates = append(decision.Candidates, StrategyNativeStream)
		}
		decision.Reason = "both endpoints prove built-in BACKUP/RESTORE support; Native stream remains the compatibility fallback"
		return decision
	}

	if capabilities.BothSupport(CapabilityNativeFormat) {
		decision.Selected = StrategyNativeStream
		decision.Candidates = []StrategyID{StrategyNativeStream}
		decision.Reason = "newer whole-table capability is not proven on both endpoints; use the stable Native-format compatibility path"
		return decision
	}

	decision.Reason = "no migration transport capability is proven on both endpoints"
	return decision
}
