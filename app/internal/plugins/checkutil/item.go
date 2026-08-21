package checkutil

import (
	"time"

	"setpoint/internal/task"
)

type Definition struct {
	ID                  string
	Name                string
	Recommended         string
	Risk                string
	Description         string
	Remediation         string
	MayAffectConnection bool
	MayAffectBusiness   bool
	SourceRefs          []string
}

func Value(definition Definition, current string, compliant bool, evidence string, executedAt time.Time) task.CheckItem {
	status := task.ItemUnsafe
	if compliant {
		status = task.ItemSafe
	}
	return task.CheckItem{
		ID: definition.ID, Status: status, Name: definition.Name,
		CurrentValue: current, RecommendedValue: definition.Recommended, Compliant: &compliant,
		Risk: definition.Risk, RiskDescription: definition.Description, Remediation: definition.Remediation,
		EvidenceSummary: evidence, Applicable: true, ExecutedAt: executedAt,
		MayAffectConnection: definition.MayAffectConnection, MayAffectBusiness: definition.MayAffectBusiness,
	}
}

func Error(definition Definition, code, message, evidence string, executedAt time.Time) task.CheckItem {
	return task.CheckItem{
		ID: definition.ID, Status: task.ItemError, Name: definition.Name,
		CurrentValue: "unavailable", RecommendedValue: definition.Recommended,
		Risk: definition.Risk, RiskDescription: definition.Description, Remediation: definition.Remediation,
		EvidenceSummary: evidence, Applicable: true, ExecutedAt: executedAt,
		MayAffectConnection: definition.MayAffectConnection, MayAffectBusiness: definition.MayAffectBusiness,
		Error: &task.Failure{Code: code, Message: message},
	}
}
func ManualReview(definition Definition, current, reason, evidence string, executedAt time.Time) task.CheckItem {
	return task.CheckItem{
		ID: definition.ID, Status: task.ItemManualReview, Name: definition.Name,
		CurrentValue: current, RecommendedValue: definition.Recommended,
		Risk: definition.Risk, RiskDescription: definition.Description, Remediation: definition.Remediation,
		EvidenceSummary: evidence, ReviewReason: reason, Applicable: true, ExecutedAt: executedAt,
		MayAffectConnection: definition.MayAffectConnection, MayAffectBusiness: definition.MayAffectBusiness,
	}
}

func NotApplicable(definition Definition, reason string, executedAt time.Time) task.CheckItem {
	return task.CheckItem{
		ID: definition.ID, Status: task.ItemNotApplicable, Name: definition.Name,
		CurrentValue: "not applicable", RecommendedValue: definition.Recommended,
		Risk: definition.Risk, RiskDescription: definition.Description, Remediation: definition.Remediation,
		EvidenceSummary: reason, Applicable: false, ExecutedAt: executedAt,
		MayAffectConnection: definition.MayAffectConnection, MayAffectBusiness: definition.MayAffectBusiness,
	}
}
