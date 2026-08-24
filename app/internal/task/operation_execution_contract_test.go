package task

import (
	"encoding/json"
	"testing"

	"setpoint/internal/operation"
)

func TestOperationExecutionContractBoundedActionsAndDigest(t *testing.T) {
	base := OperationExecutionContract{
		OperationID: "operation.test",
		RunID:       "run-1",
		PlanDigest:  "sha256:plan",
		Targets:     []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}},
		Plan:        operation.Plan{SchemaVersion: "test.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.exec.v1", Payload: json.RawMessage(`{}`)}},
	}
	for _, action := range []OperationAction{OperationActionCreateRestorePoint, OperationActionApply, OperationActionVerify, OperationActionRollback, OperationActionVerifyRollback} {
		t.Run(string(action), func(t *testing.T) {
			candidate := base
			candidate.Action = action
			switch action {
			case OperationActionApply:
				candidate.RestorePoint = &operation.RestorePoint{ID: "rp"}
			case OperationActionVerify:
				candidate.Apply = &operation.ApplyResult{}
			case OperationActionRollback:
				candidate.RestorePoint = &operation.RestorePoint{ID: "rp"}
			case OperationActionVerifyRollback:
				candidate.RestorePoint = &operation.RestorePoint{ID: "rp"}
				candidate.Rollback = &operation.RollbackResult{}
			}
			contract, digest, err := NewOperationExecutionContract(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if contract.Action != action || digest == "" {
				t.Fatalf("contract=%#v digest=%q", contract, digest)
			}
			if err := ValidateOperationExecutionContract(contract, digest); err != nil {
				t.Fatal(err)
			}
			contract.PlanDigest = "sha256:stale"
			if err := ValidateOperationExecutionContract(contract, digest); err == nil {
				t.Fatal("mutated contract digest was accepted")
			}
		})
	}
}

func TestOperationExecutionContractRejectsUnknownAction(t *testing.T) {
	_, _, err := NewOperationExecutionContract(OperationExecutionContract{
		OperationID: "operation.test", RunID: "run-1", Action: "entire_lifecycle", PlanDigest: "sha256:plan",
		Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}},
		Plan:    operation.Plan{SchemaVersion: "test.plan.v1", Execution: operation.Artifact{SchemaVersion: "test.exec.v1", Payload: json.RawMessage(`{}`)}},
	})
	if err == nil {
		t.Fatal("unknown action was accepted")
	}
}
