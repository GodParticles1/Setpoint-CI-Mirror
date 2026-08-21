package app

import (
	"encoding/json"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/task"
)

func TestAwaitingConfirmationRequiresCompletePlanningEvidence(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	resource := task.Resource{Kind: task.KindOperationPlanningTask,
		Spec: task.Spec{OperationID: "operation.test", OperationVersion: "1.0.0", CapabilityDigest: "sha256:cap",
			Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}, Parameters: json.RawMessage(`{}`)},
		Status: task.Status{Phase: task.PhaseRunning}}
	discovery := operation.Discovery{Applicable: true, Summary: "discovered", Targets: []operation.Target{{Kind: operation.TargetDataObject, Component: "test", Resource: "db.table"}}, Snapshot: operation.Artifact{SchemaVersion: "test.discovery.v1", Payload: json.RawMessage(`{}`)}}
	precheck := operation.Precheck{Passed: true, Summary: "passed", Snapshot: operation.Artifact{SchemaVersion: "test.precheck.v1", Payload: json.RawMessage(`{}`)}}
	plan := operation.Plan{SchemaVersion: "test.plan.v1", Summary: "plan", Steps: []operation.PlanStep{}, Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}
	impact := operation.Impact{Summary: "impact", Risk: operation.RiskLow, Changes: []operation.Change{}}
	digest, err := operation.PlanDigest(resource.Spec.CapabilityDigest, resource.Spec.Targets, resource.Spec.Parameters, nil, plan, impact)
	if err != nil {
		t.Fatal(err)
	}
	valid := operation.PlanningResult{OperationID: resource.Spec.OperationID, OperationVersion: resource.Spec.OperationVersion,
		CapabilityDigest: resource.Spec.CapabilityDigest, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready",
		StartedAt: now, CompletedAt: now.Add(time.Second), Discovery: &discovery, Precheck: &precheck, Plan: &plan, Impact: &impact, PlanDigest: digest}
	if err := validateOperationPlanningResult(resource, &task.ResultSubmission{Phase: task.PhaseSucceeded, OperationResult: &valid}); err != nil {
		t.Fatalf("complete evidence rejected: %v", err)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*operation.PlanningResult)
	}{
		{name: "missing discovery", mutate: func(result *operation.PlanningResult) { result.Discovery = nil }},
		{name: "not applicable", mutate: func(result *operation.PlanningResult) { result.Discovery.Applicable = false }},
		{name: "missing precheck", mutate: func(result *operation.PlanningResult) { result.Precheck = nil }},
		{name: "failed precheck", mutate: func(result *operation.PlanningResult) { result.Precheck.Passed = false }},
		{name: "wrong checkpoint", mutate: func(result *operation.PlanningResult) { result.Checkpoint = "planned" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := valid
			discoveryCopy, precheckCopy := discovery, precheck
			candidate.Discovery, candidate.Precheck = &discoveryCopy, &precheckCopy
			testCase.mutate(&candidate)
			if err := validateOperationPlanningResult(resource, &task.ResultSubmission{Phase: task.PhaseSucceeded, OperationResult: &candidate}); err == nil {
				t.Fatal("incomplete planning evidence accepted")
			}
		})
	}
}
