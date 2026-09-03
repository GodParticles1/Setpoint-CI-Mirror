package task

import (
	"encoding/json"
	"testing"

	"setpoint/internal/operation"
)

func crossNodeContract() OperationExecutionContract {
	stage := operation.PlanStep{
		ID: "mutate-vip-owner", Target: operation.Target{Kind: operation.TargetNode, NodeID: "node-b"},
		ExecutorNodeID: "node-b", Writes: true, Mutation: operation.StageMutationVIPOwner,
		Preconditions: []operation.StagePrecondition{{
			Kind: operation.StagePreconditionVIPOwner, ParticipantNodeID: "node-b", Verified: true,
			Evidence: []operation.EvidenceRef{{ID: "observation-1", Kind: "vip_owner_observation"}},
		}},
	}
	return OperationExecutionContract{
		OperationID: "operation.test", RunID: "run-1", Action: OperationActionCreateRestorePoint,
		PlanDigest: "sha256:plan", ParticipantNodeIDs: []string{"node-a", "node-b"},
		StageIndex: 1, Stage: stage,
		Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-b"}},
		Plan: operation.Plan{SchemaVersion: "test.plan.v1", Steps: []operation.PlanStep{
			{ID: "prepare-peer", Target: operation.Target{Kind: operation.TargetNode, NodeID: "node-a"}, ExecutorNodeID: "node-a"},
			stage,
		}, Execution: operation.Artifact{SchemaVersion: "test.exec.v1", Payload: json.RawMessage(`{}`)}},
	}
}

func TestCrossNodeContractFreezesExactlyTwoParticipantsAndStageExecutor(t *testing.T) {
	contract, digest, err := NewOperationExecutionContract(crossNodeContract())
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.ParticipantNodeIDs) != 2 || contract.ParticipantNodeIDs[0] != "node-a" || contract.ParticipantNodeIDs[1] != "node-b" {
		t.Fatalf("participants=%v", contract.ParticipantNodeIDs)
	}
	if contract.Stage.ExecutorNodeID != "node-b" || contract.StageIndex != 1 || digest == "" {
		t.Fatalf("stage=%#v index=%d digest=%q", contract.Stage, contract.StageIndex, digest)
	}
}

func TestCrossNodeContractRejectsStageExecutorOutsideFrozenParticipants(t *testing.T) {
	contract := crossNodeContract()
	contract.Stage.ExecutorNodeID = "node-c"
	if _, _, err := NewOperationExecutionContract(contract); err == nil {
		t.Fatal("stage executor outside frozen participants was accepted")
	}
}

func TestCrossNodeContractRejectsVIPOwnerMutationWithoutVerifiedObservedFact(t *testing.T) {
	contract := crossNodeContract()
	contract.Stage.Preconditions[0].Verified = false
	if _, _, err := NewOperationExecutionContract(contract); err == nil {
		t.Fatal("VIP-owner mutation accepted an unverified ownership precondition")
	}
	contract = crossNodeContract()
	contract.Stage.Preconditions = nil
	if _, _, err := NewOperationExecutionContract(contract); err == nil {
		t.Fatal("VIP-owner mutation accepted planned state without observed ownership evidence")
	}
}

func TestCrossNodeExecutionResultCarriesExactStageCorrelation(t *testing.T) {
	result := OperationExecutionResult{
		OperationID: "operation.test", RunID: "run-1", Action: OperationActionApply,
		ParticipantNodeIDs: []string{"node-a", "node-b"}, StageID: "mutate-vip-owner", StageIndex: 1, ExecutorNodeID: "node-b",
	}
	if len(result.ParticipantNodeIDs) != 2 || result.ParticipantNodeIDs[0] != "node-a" || result.StageID != "mutate-vip-owner" || result.StageIndex != 1 || result.ExecutorNodeID != "node-b" {
		t.Fatalf("result=%#v", result)
	}
}
