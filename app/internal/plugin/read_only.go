package plugin

import (
	"context"
	"encoding/json"

	"setpoint/internal/executor"
	"setpoint/internal/task"
)

type CheckInput struct {
	Executor         executor.CommandExecutor
	Parameters       json.RawMessage
	System           string
	SelectedCheckIDs []string
}

type Detection struct {
	Applicable bool
	Reason     string
}

// CheckDefinition is an observation-only capability. It must not change files,
// services, kernel settings, accounts, or other host state.
type CheckDefinition interface {
	CheckDescriptor
	Detect(context.Context, CheckInput) (Detection, error)
	Check(context.Context, CheckInput) ([]task.CheckItem, error)
}
