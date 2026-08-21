package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"setpoint/internal/task"
)

func TestExecuteCheckEnforcesRegisteredItemContract(t *testing.T) {
	validItem := func(id string) task.CheckItem {
		compliant := true
		return task.CheckItem{
			ID: id, Name: id, Status: task.ItemSafe, Applicable: true,
			Compliant: &compliant, ExecutedAt: time.Now().UTC(),
		}
	}
	tests := []struct {
		name  string
		items []task.CheckItem
	}{
		{name: "missing item", items: nil},
		{name: "unknown item", items: []task.CheckItem{validItem("kernel.setting"), validItem("unknown.setting")}},
		{name: "duplicate item", items: []task.CheckItem{validItem("kernel.setting"), validItem("kernel.setting")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewCheckRegistry()
			candidate := &testReadOnlyPlugin{metadata: testMetadata("contract.test"), items: test.items}
			if err := registry.Register(candidate); err != nil {
				t.Fatal(err)
			}
			result, err := ExecuteCheck(context.Background(), registry, "contract.test", CheckInput{Executor: stubExecutor{}, System: "linux"})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != task.CheckError || result.Error == nil || result.Error.Code != "check_result_invalid" || len(result.Items) != 0 {
				t.Fatalf("invalid lifecycle result = %#v", result)
			}
		})
	}
}

func TestExecuteCheckAllowsKnownPartialItemsOnExecutionError(t *testing.T) {
	metadata := testMetadata("partial.error")
	metadata.Checks = append(metadata.Checks, CheckItemDefinition{
		ID: "second.setting", Name: "Second setting", RecommendedValue: "secure",
	})
	compliant := true
	candidate := &testReadOnlyPlugin{
		metadata: metadata,
		items: []task.CheckItem{{
			ID: "kernel.setting", Name: "Kernel setting", Status: task.ItemSafe,
			Applicable: true, Compliant: &compliant, ExecutedAt: time.Now().UTC(),
		}},
		err: errors.New("second observation failed"),
	}
	registry := NewCheckRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteCheck(context.Background(), registry, "partial.error", CheckInput{Executor: stubExecutor{}, System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != task.CheckError || result.Error == nil || result.Error.Code != "check_execution_failed" || len(result.Items) != 1 {
		t.Fatalf("partial error lifecycle result = %#v", result)
	}
}
