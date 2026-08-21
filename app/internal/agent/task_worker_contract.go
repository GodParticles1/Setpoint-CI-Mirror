package agent

import (
	"context"
	"fmt"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func (worker *TaskWorker) executeTaskCheck(ctx context.Context, resource task.Resource) (task.CheckResult, error) {
	commandExecutor := worker.executor
	if resource.Spec.Execution != nil {
		configured, err := executor.WithTrustedExecutableRoots(
			worker.executor, resource.Spec.Execution.TrustedExecutableRoots,
		)
		if err != nil {
			return task.CheckResult{}, fmt.Errorf("configure frozen trusted executable roots: %w", err)
		}
		commandExecutor = configured
	}
	input := plugin.CheckInput{
		Executor: commandExecutor, Parameters: resource.Spec.Parameters, System: worker.system,
	}
	if resource.Spec.Execution == nil {
		return plugin.ExecuteCheck(ctx, worker.registry, resource.Spec.PluginID, input)
	}
	return plugin.ExecuteFrozenCheck(ctx, worker.registry, *resource.Spec.Execution, resource.Spec.ContractDigest, input)
}

func (worker *TaskWorker) taskPluginIdentity(resource task.Resource) (string, string) {
	if resource.Spec.Execution != nil {
		return resource.Spec.Execution.PluginID, resource.Spec.Execution.PluginVersion
	}
	return resource.Spec.PluginID, worker.pluginVersion(resource.Spec.PluginID)
}
