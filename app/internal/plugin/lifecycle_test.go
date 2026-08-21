package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/task"
)

func TestExecuteReadOnlyCompletesStructuredResult(t *testing.T) {
	registry := NewCheckRegistry()
	candidate := &testReadOnlyPlugin{metadata: testMetadata("system.info")}
	compliant := true
	candidate.items = []task.CheckItem{{ID: "kernel.setting", Name: "Kernel setting", Applicable: true, Compliant: &compliant, ExecutedAt: time.Now().UTC()}}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	candidate.metadata.ID = "mutated.id"
	candidate.metadata.Version = "9.9.9"
	result, err := ExecuteCheck(context.Background(), registry, "system.info", CheckInput{Executor: stubExecutor{}, System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != task.CheckCompleted || result.Error != nil || len(result.Items) != 1 ||
		result.PluginID != "system.info" || result.PluginVersion != "1.0.0" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecuteReadOnlyClassifiesCommandAndSystemFailures(t *testing.T) {
	registry := NewCheckRegistry()
	candidate := &testReadOnlyPlugin{metadata: testMetadata("system.info")}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	candidate.err = &executor.Error{Kind: executor.ErrorTimeout, Err: context.DeadlineExceeded}
	result, err := ExecuteCheck(context.Background(), registry, "system.info", CheckInput{Executor: stubExecutor{}, System: "linux"})
	if err != nil || result.State != task.CheckError || result.Error == nil || result.Error.Code != string(executor.ErrorTimeout) {
		t.Fatalf("timeout result=%#v err=%v", result, err)
	}
	result, err = ExecuteCheck(context.Background(), registry, "system.info", CheckInput{Executor: stubExecutor{}, System: "windows"})
	if err != nil || result.State != task.CheckCompleted || result.Error != nil || len(result.Items) != 1 ||
		result.Items[0].Status != task.ItemNotApplicable {
		t.Fatalf("system result=%#v err=%v", result, err)
	}
}

func TestExecuteReadOnlyRejectsMetadataOnlyAndInvalidItems(t *testing.T) {
	registry := NewCheckRegistry()
	if err := registry.Register(NewMetadataDescriptor(testMetadata("metadata.only"))); err != nil {
		t.Fatal(err)
	}
	_, err := ExecuteCheck(context.Background(), registry, "metadata.only", CheckInput{Executor: stubExecutor{}, System: "linux"})
	if !errors.Is(err, ErrCheckExecutionUnavailable) {
		t.Fatalf("metadata-only error=%v", err)
	}
	invalidMetadata := testMetadata("invalid.result")
	invalidMetadata.Checks[0].ID = "missing-time"
	candidate := &testReadOnlyPlugin{metadata: invalidMetadata, items: []task.CheckItem{{ID: "missing-time", Name: "Missing time"}}}
	if err := registry.Register(candidate); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteCheck(context.Background(), registry, "invalid.result", CheckInput{Executor: stubExecutor{}, System: "linux"})
	if err != nil || result.Error == nil || result.Error.Code != "check_result_invalid" {
		t.Fatalf("invalid item result=%#v err=%v", result, err)
	}
}

type testReadOnlyPlugin struct {
	metadata Metadata
	items    []task.CheckItem
	err      error
}

func (candidate *testReadOnlyPlugin) Metadata() Metadata { return candidate.metadata }

func (candidate *testReadOnlyPlugin) Detect(context.Context, CheckInput) (Detection, error) {
	return Detection{Applicable: true}, nil
}

func (candidate *testReadOnlyPlugin) Check(context.Context, CheckInput) ([]task.CheckItem, error) {
	return candidate.items, candidate.err
}

type stubExecutor struct{}

func (stubExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	return executor.Result{}, nil
}
