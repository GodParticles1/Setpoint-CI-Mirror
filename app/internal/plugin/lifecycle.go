package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/task"
)

var ErrCheckExecutionUnavailable = errors.New("check does not provide executable observation logic")

func ExecuteCheck(ctx context.Context, registry *CheckRegistry, pluginID string, input CheckInput) (task.CheckResult, error) {
	if registry == nil {
		return task.CheckResult{}, errors.New("check registry is required")
	}
	if input.Executor == nil {
		return task.CheckResult{}, errors.New("command executor is required")
	}
	definition, exists := registry.Definition(pluginID)
	if !exists {
		return task.CheckResult{}, fmt.Errorf("%w: %s", ErrCheckExecutionUnavailable, pluginID)
	}
	metadata, _ := registry.Get(pluginID)
	if len(input.SelectedCheckIDs) > 0 {
		selected, err := selectedDefinitions(metadata, input.SelectedCheckIDs)
		if err != nil {
			return task.CheckResult{}, fmt.Errorf("select check definitions: %w", err)
		}
		metadata.Checks = selected
	}
	startedAt := time.Now().UTC()
	result := task.CheckResult{
		PluginID: metadata.ID, PluginVersion: metadata.Version, State: task.CheckCompleted,
		StartedAt: startedAt, Items: []task.CheckItem{},
	}
	contract := ResultContract(metadata)
	if !supportsSystem(metadata.SupportedSystems, input.System) {
		result.Items = conclusionItems(metadata, task.ItemNotApplicable,
			"Agent operating system is outside this check's supported systems", nil, time.Now().UTC())
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}

	detection, detectErr := definition.Detect(ctx, input)
	if detectErr != nil {
		failure := &task.Failure{Code: classifyCheckError(detectErr), Message: detectErr.Error()}
		result.Items = conclusionItems(metadata, task.ItemError, "Component detection failed", failure, time.Now().UTC())
		result.State = task.CheckError
		result.Error = failure
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}
	if !detection.Applicable {
		reason := strings.TrimSpace(detection.Reason)
		if reason == "" {
			reason = "Required component is not installed or active"
		}
		result.Items = conclusionItems(metadata, task.ItemNotApplicable, reason, nil, time.Now().UTC())
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}

	items, checkErr := definition.Check(ctx, input)
	result.Items = filterSelectedItems(items, input.SelectedCheckIDs)
	result.CompletedAt = time.Now().UTC()
	if checkErr != nil {
		result.State = task.CheckError
		result.Error = &task.Failure{Code: classifyCheckError(checkErr), Message: checkErr.Error()}
	}
	if validationErr := task.ValidateCheckResult(&result, contract); validationErr != nil {
		result.Items = []task.CheckItem{}
		result.State = task.CheckError
		result.Error = &task.Failure{Code: "check_result_invalid", Message: validationErr.Error()}
	}
	return result, nil
}

func conclusionItems(metadata Metadata, status task.ItemStatus, evidence string, failure *task.Failure, executedAt time.Time) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(metadata.Checks))
	for _, definition := range metadata.Checks {
		item := task.CheckItem{
			ID: definition.ID, Status: status, Name: definition.Name,
			RecommendedValue: definition.RecommendedValue, Risk: string(metadata.Risk),
			RiskDescription: definition.Description, Remediation: "No automatic remediation is provided",
			EvidenceSummary: evidence, ExecutedAt: executedAt,
		}
		switch status {
		case task.ItemNotApplicable:
			item.Applicable = false
			item.CurrentValue = "not applicable"
		case task.ItemError:
			item.Applicable = true
			item.CurrentValue = "unavailable"
			if failure != nil {
				copy := *failure
				item.Error = &copy
			}
		}
		items = append(items, item)
	}
	return items
}

func supportsSystem(supported []string, current string) bool {
	current = strings.TrimSpace(current)
	for _, system := range supported {
		if strings.EqualFold(strings.TrimSpace(system), current) {
			return true
		}
	}
	return false
}

func filterSelectedItems(items []task.CheckItem, selectedIDs []string) []task.CheckItem {
	if len(selectedIDs) == 0 {
		return items
	}
	selected := make(map[string]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = struct{}{}
	}
	result := make([]task.CheckItem, 0, len(selected))
	for _, item := range items {
		if _, exists := selected[item.ID]; exists {
			result = append(result, item)
		}
	}
	return result
}

func classifyCheckError(err error) string {
	var executionError *executor.Error
	if errors.As(err, &executionError) {
		return string(executionError.Kind)
	}
	if errors.Is(err, context.Canceled) {
		return string(executor.ErrorCanceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return string(executor.ErrorTimeout)
	}
	return "check_execution_failed"
}
