package plugin

import (
	"context"
	"fmt"

	"setpoint/internal/task"
)

func ExecuteFrozenCheck(
	ctx context.Context,
	registry *CheckRegistry,
	contract task.CheckExecutionContract,
	digest string,
	input CheckInput,
) (task.CheckResult, error) {
	if _, err := MetadataForExecutionContract(registry, contract, digest); err != nil {
		return task.CheckResult{}, err
	}
	input.Parameters = append([]byte(nil), contract.Parameters...)
	input.SelectedCheckIDs = make([]string, 0, len(contract.Checks))
	for _, definition := range contract.Checks {
		input.SelectedCheckIDs = append(input.SelectedCheckIDs, definition.ID)
	}
	result, err := ExecuteCheck(ctx, registry, contract.PluginID, input)
	if err != nil {
		return task.CheckResult{}, err
	}
	selected := make(map[string]struct{}, len(contract.Checks))
	for _, definition := range contract.Checks {
		selected[definition.ID] = struct{}{}
	}
	items := make([]task.CheckItem, 0, len(selected))
	for _, item := range result.Items {
		if _, exists := selected[item.ID]; exists {
			items = append(items, item)
		}
	}
	result.Items = items
	if err := task.ValidateCheckResult(&result, contract.ResultContract()); err != nil {
		result.Items = []task.CheckItem{}
		result.State = task.CheckError
		result.Error = &task.Failure{Code: "check_result_invalid", Message: fmt.Sprintf("frozen result contract: %v", err)}
	}
	return result, nil
}
