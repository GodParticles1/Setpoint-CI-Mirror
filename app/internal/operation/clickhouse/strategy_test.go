package clickhouse

import "testing"

func TestStrategySelectorPrefersNativeForTimeRange(t *testing.T) {
	decision := NewStrategySelector().Select(Parameters{StartTime: "2026-08-01T00:00:00+08:00", EndTime: "2026-08-02T00:00:00+08:00"}, Snapshot{}, Snapshot{}, PrecheckReport{Compatible: true})
	if decision.Selected != StrategyNativeStream { t.Fatalf("selected=%q", decision.Selected) }
	if len(decision.Candidates) != 1 || decision.Candidates[0] != StrategyNativeStream { t.Fatalf("candidates=%v", decision.Candidates) }
}

func TestStrategySelectorKeepsBackupAsCapabilityCheckedCandidate(t *testing.T) {
	decision := NewStrategySelector().Select(Parameters{}, Snapshot{}, Snapshot{}, PrecheckReport{Compatible: true})
	if decision.Selected != StrategyNativeStream { t.Fatalf("selected=%q", decision.Selected) }
	if len(decision.Candidates) != 2 || decision.Candidates[1] != StrategyBuiltinBackupRestore { t.Fatalf("candidates=%v", decision.Candidates) }
	if len(decision.RequiresCapabilityChecks) != 1 || decision.RequiresCapabilityChecks[0] != "clickhouse_backup_restore_support" { t.Fatalf("checks=%v", decision.RequiresCapabilityChecks) }
}
