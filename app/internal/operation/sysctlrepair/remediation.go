package sysctlrepair

import (
	"errors"
	"setpoint/internal/plugin"
	"setpoint/internal/remediation"
	"setpoint/internal/task"
)

// RemediationBinding preserves the four reviewed runtime-only AUTO_SAFE repairs.
func RemediationBinding() remediation.Binding {
	return remediation.Binding{
		CheckIDs: append([]string(nil), checkOptions...), OperationID: ID,
		Disposition: plugin.RemediationAutoSafe, SupportsRollback: true,
		Parameters: func(item task.CheckItem) (map[string]string, error) {
			if _, ok := allowedChecks[item.ID]; !ok || item.Status != task.ItemUnsafe || item.RecommendedValue != "runtime=0; persisted=0" || item.CurrentValue != "runtime=1; persisted=0" || item.MayAffectConnection || item.MayAffectBusiness || item.RequiresRestart {
				return nil, errors.New("finding is not a reviewed runtime-only ICMP redirect repair")
			}
			return map[string]string{"check_id": item.ID, "target_value": item.RecommendedValue}, nil
		},
	}
}
