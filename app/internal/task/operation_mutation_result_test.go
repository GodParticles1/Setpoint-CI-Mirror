package task

import (
	"encoding/json"
	"reflect"
	"testing"

	"setpoint/internal/operation"
)

func TestMutationResultCompatibilityAndStrictOptIn(t *testing.T) {
	for _, action := range []OperationAction{OperationActionApply, OperationActionRollback} {
		for _, phase := range []Phase{PhaseSucceeded, PhaseFailed} {
			for _, state := range []operation.MutationState{"", operation.MutationNotStarted, operation.MutationChanged, operation.MutationMayHaveChanged, "unknown"} {
				for _, reconnect := range []bool{false, true} {
					var handoff *operation.ReconnectHandoff
					if reconnect {
						handoff = &operation.ReconnectHandoff{Barrier: operation.StageBarrierAgentReconnect, Reboot: true, BootIDBefore: "before"}
					}
					result := OperationExecutionResult{Action: action}
					if action == OperationActionApply {
						result.Apply = &operation.ApplyResult{MutationState: state, Reconnect: handoff}
					} else {
						result.Rollback = &operation.RollbackResult{MutationState: state, Reconnect: handoff}
					}
					before, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					wantError := state == "unknown" || state == "" && (reconnect || action == OperationActionRollback && phase == PhaseFailed)
					if err := ValidateOperationMutationEvidence(result, phase); (err != nil) != wantError {
						t.Fatalf("action=%s phase=%s state=%q reconnect=%v err=%v", action, phase, state, reconnect, err)
					}
					after, err := json.Marshal(result)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatal("validation changed evidence")
					}
				}
			}
		}
	}
}

func TestMutationResultRejectsInvalidReconnectShape(t *testing.T) {
	for _, mutate := range []func(*operation.ReconnectHandoff){
		func(h *operation.ReconnectHandoff) { h.Barrier = "unknown" },
		func(h *operation.ReconnectHandoff) { h.Reboot = false },
		func(h *operation.ReconnectHandoff) { h.BootIDBefore = "" },
		func(h *operation.ReconnectHandoff) { h.BootIDAfter = h.BootIDBefore },
	} {
		handoff := &operation.ReconnectHandoff{Barrier: operation.StageBarrierAgentReconnect, Reboot: true, BootIDBefore: "before"}
		mutate(handoff)
		result := OperationExecutionResult{Action: OperationActionApply, Apply: &operation.ApplyResult{MutationState: operation.MutationChanged, Reconnect: handoff}}
		if err := ValidateOperationMutationEvidence(result, PhaseSucceeded); err == nil {
			t.Fatalf("invalid handoff accepted: %#v", handoff)
		}
	}
}

func TestMutationStateWirePresenceIsStrict(t *testing.T) {
	for _, action := range []string{"apply", "rollback"} {
		for _, value := range []string{`""`, `null`, `"invalid"`, `true`, `1`} {
			raw := []byte(`{"action":"` + action + `","` + action + `":{"mutation_state":` + value + `}}`)
			var result OperationExecutionResult
			if err := json.Unmarshal(raw, &result); err == nil {
				t.Fatalf("explicit invalid state accepted: %s", raw)
			}
		}
		var legacy OperationExecutionResult
		if err := json.Unmarshal([]byte(`{"action":"`+action+`","`+action+`":{}}`), &legacy); err != nil {
			t.Fatal(err)
		}
		if err := ValidateOperationMutationEvidence(legacy, PhaseSucceeded); err != nil {
			t.Fatalf("legacy omission rejected: %v", err)
		}
	}
}
