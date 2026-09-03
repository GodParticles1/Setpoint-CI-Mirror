package operationrun

import (
	"errors"
	"fmt"

	"setpoint/internal/operation"
)

const SingleNodeStageID = "single-node"

func ExecutionStages(run Resource) ([]operation.PlanStep, error) {
	participants := run.Spec.ParticipantNodeIDs
	if len(participants) == 0 && run.Spec.NodeID != "" {
		participants = []string{run.Spec.NodeID}
	}
	if len(participants) == 0 {
		return nil, errors.New("operation run has no frozen participants")
	}
	if len(participants) == 1 {
		stage := operation.PlanStep{
			ID: SingleNodeStageID, Name: "single-node operation", ExecutorNodeID: participants[0],
			Target: operation.Target{Kind: operation.TargetNode, NodeID: participants[0]},
		}
		return []operation.PlanStep{stage}, operation.ValidateExecutionStage(stage, participants)
	}
	if run.Plan == nil || len(run.Plan.Steps) == 0 {
		return nil, errors.New("multi-node operation requires ordered physical plan stages")
	}
	stages := append([]operation.PlanStep(nil), run.Plan.Steps...)
	seen := make(map[string]struct{}, len(stages))
	for index, stage := range stages {
		if err := operation.ValidateExecutionStage(stage, participants); err != nil {
			return nil, fmt.Errorf("plan stage %d: %w", index, err)
		}
		if _, exists := seen[stage.ID]; exists {
			return nil, fmt.Errorf("plan contains duplicate physical stage ID %q", stage.ID)
		}
		seen[stage.ID] = struct{}{}
	}
	return stages, nil
}

func StageTargets(run Resource, stage operation.PlanStep) []operation.Target {
	if len(run.Spec.ParticipantNodeIDs) <= 1 {
		targets := make([]operation.Target, 0, len(run.Spec.Targets))
		seen := make(map[operation.Target]struct{})
		appendTarget := func(target operation.Target) {
			if _, exists := seen[target]; exists {
				return
			}
			seen[target] = struct{}{}
			targets = append(targets, target)
		}
		for _, target := range run.Spec.Targets {
			appendTarget(target)
		}
		if run.Plan != nil {
			for _, step := range run.Plan.Steps {
				appendTarget(step.Target)
			}
		}
		return targets
	}
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: stage.ExecutorNodeID}}
	if stage.Target != targets[0] {
		targets = append(targets, stage.Target)
	}
	return targets
}
