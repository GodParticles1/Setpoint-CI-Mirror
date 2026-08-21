package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"setpoint/internal/task"
)

func TestExecuteFrozenCheckReturnsOnlySelectedItems(t *testing.T) {
	metadata := testMetadata("selection.test")
	metadata.Checks = append(metadata.Checks, CheckItemDefinition{
		ID: "second.setting", Name: "Second", RecommendedValue: "secure",
	})
	compliant := true
	candidate := &testReadOnlyPlugin{metadata: metadata, items: []task.CheckItem{
		{ID: "kernel.setting", Name: "Kernel", Status: task.ItemSafe, Applicable: true, Compliant: &compliant, ExecutedAt: time.Now().UTC()},
		{ID: "second.setting", Name: "Second", RecommendedValue: "secure", Status: task.ItemSafe, Applicable: true, Compliant: &compliant, ExecutedAt: time.Now().UTC()},
	}}
	registry := NewCheckRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	contract, digest, err := FreezeExecutionContract(metadata, []string{"second.setting"}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteFrozenCheck(context.Background(), registry, contract, digest, CheckInput{Executor: stubExecutor{}, System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != task.CheckCompleted || len(result.Items) != 1 || result.Items[0].ID != "second.setting" {
		t.Fatalf("selected result=%#v", result)
	}
}

func TestFreezeExecutionContractDeduplicatesSelectedChecks(t *testing.T) {
	metadata := testMetadata("deduplicate.selection")
	contract, _, err := FreezeExecutionContract(
		metadata,
		[]string{"kernel.setting", "kernel.setting"},
		json.RawMessage("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.Checks) != 1 || contract.Checks[0].ID != "kernel.setting" {
		t.Fatalf("deduplicated contract=%#v", contract)
	}
}

func TestExecuteFrozenCheckRejectsRegistryVersionOrDefinitionDrift(t *testing.T) {
	original := testMetadata("drift.test")
	contract, digest, err := FreezeExecutionContract(original, nil, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Metadata){
		func(metadata *Metadata) { metadata.Version = "2.0.0" },
		func(metadata *Metadata) { metadata.Checks[0].RecommendedValue = "changed" },
	} {
		metadata := cloneMetadata(original)
		mutate(&metadata)
		registry := NewCheckRegistry()
		if err := registry.Register(&testReadOnlyPlugin{metadata: metadata}); err != nil {
			t.Fatal(err)
		}
		_, err := ExecuteFrozenCheck(context.Background(), registry, contract, digest, CheckInput{Executor: stubExecutor{}, System: "linux"})
		if !errors.Is(err, ErrCheckContractMismatch) {
			t.Fatalf("drift error=%v", err)
		}
	}
}
