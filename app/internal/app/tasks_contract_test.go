package app

import (
	"encoding/json"
	"testing"
	"time"

	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestResultContractUsesFrozenTaskWhenRegistryIsUnavailable(t *testing.T) {
	metadata := plugin.Metadata{
		ID: "test.frozen", Category: "test", Name: "Frozen", Version: "1.0.0", Description: "test",
		Mode: plugin.ModeReadOnly, Risk: plugin.RiskLow, Impact: "none", SupportedSystems: []string{"linux"},
		Checks: []plugin.CheckItemDefinition{{ID: "test.item", Name: "Item", RecommendedValue: "secure"}},
	}
	contract, digest, err := plugin.FreezeExecutionContract(metadata, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{checks: plugin.NewCheckRegistry()}
	resource := task.Resource{Spec: task.Spec{
		PluginID: metadata.ID, Execution: &contract, ContractDigest: digest,
	}}
	resultContract, err := service.resultContract(resource)
	if err != nil || resultContract.PluginVersion != metadata.Version || len(resultContract.ItemDefinitions) != 1 {
		t.Fatalf("frozen result contract=%#v err=%v", resultContract, err)
	}
	compliant := true
	result := task.CheckResult{
		PluginID: metadata.ID, PluginVersion: metadata.Version, State: task.CheckCompleted,
		StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC().Add(time.Millisecond),
		Items: []task.CheckItem{{
			ID: "test.item", Name: "changed after task creation", RecommendedValue: "secure",
			Status: task.ItemSafe, Applicable: true, Compliant: &compliant, ExecutedAt: time.Now().UTC(),
		}},
	}
	if err := task.ValidateCheckResult(&result, resultContract); err == nil {
		t.Fatal("result definition drift was accepted")
	}
}
