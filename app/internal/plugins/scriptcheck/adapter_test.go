package scriptcheck

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

type stubExecutor struct {
	result  executor.Result
	err     error
	command executor.Command
}

func (stub *stubExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	stub.command = command
	return stub.result, stub.err
}

func metadata() plugin.Metadata {
	return plugin.Metadata{
		ID: "example.script.health", Category: "Example", Name: "Example script health",
		Version: "1.0.0", Description: "Example standalone script adapter.",
		Mode: plugin.ModeReadOnly, Risk: plugin.RiskMedium, Impact: "read-only",
		SupportedSystems: []string{"linux"},
		Parameters: []plugin.Parameter{{Name: "scope", Type: "string", Description: "bounded scope"}},
		Checks: []plugin.CheckItemDefinition{
			{ID: "example.service.running", Name: "Service running", Description: "Service must be running.", RecommendedValue: "running", SourceRefs: []string{"example:service"}},
			{ID: "example.queue.depth", Name: "Queue depth", Description: "Queue depth must remain bounded.", RecommendedValue: "<=100", SourceRefs: []string{"example:queue"}},
		},
	}
}

func TestDefinitionExecutesOnlyExplicitJSONModeAndEnrichesFrozenMetadata(t *testing.T) {
	definition, err := New(metadata(), "/opt/setpoint-tools/example-health", "--bounded")
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubExecutor{result: executor.Result{Stdout: `{
		"schema_version":"setpoint.script-check/v1",
		"items":[
			{"id":"example.service.running","status":"safe","current_value":"running","evidence_summary":"pid=42"},
			{"id":"example.queue.depth","status":"unsafe","current_value":"125","evidence_summary":"depth=125"}
		]
	}`}}
	items, err := definition.Check(context.Background(), plugin.CheckInput{
		Executor: stub, Parameters: json.RawMessage(`{"scope":"local"}`),
		SelectedCheckIDs: []string{"example.service.running", "example.queue.depth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"--bounded", "--setpoint-json-v1",
		"--setpoint-parameters-json", `{"scope":"local"}`,
		"--setpoint-check-ids-json", `["example.service.running","example.queue.depth"]`,
	}
	if stub.command.Name != "/opt/setpoint-tools/example-health" || !reflect.DeepEqual(stub.command.Args, wantArgs) {
		t.Fatalf("command=%#v", stub.command)
	}
	if stub.command.OutputLimit != outputLimit {
		t.Fatalf("output limit=%d", stub.command.OutputLimit)
	}
	if len(items) != 2 || items[0].Name != "Service running" || items[0].RecommendedValue != "running" ||
		items[0].Risk != string(plugin.RiskMedium) || items[0].SupportsAutomaticFix || items[0].SupportsRollback {
		t.Fatalf("items=%#v", items)
	}
	if items[0].Status != task.ItemSafe || items[0].Compliant == nil || !*items[0].Compliant {
		t.Fatalf("safe item=%#v", items[0])
	}
	if items[1].Status != task.ItemUnsafe || items[1].Compliant == nil || *items[1].Compliant {
		t.Fatalf("unsafe item=%#v", items[1])
	}
}

func TestDefinitionRejectsSecretLikeParametersBecauseTheyWouldBeArgvVisible(t *testing.T) {
	candidate := metadata()
	candidate.Parameters = append(candidate.Parameters, plugin.Parameter{Name: "api_token", Type: "string", Description: "forbidden"})
	if _, err := New(candidate, "example-health"); err == nil {
		t.Fatal("expected secret-like parameter rejection")
	}
}

func TestDefinitionRejectsScriptAttemptToElevateAutomaticRepair(t *testing.T) {
	definition, err := New(metadata(), "example-health")
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubExecutor{result: executor.Result{Stdout: `{
		"schema_version":"setpoint.script-check/v1",
		"items":[{
			"id":"example.service.running",
			"status":"safe",
			"current_value":"running",
			"evidence_summary":"pid=42",
			"supports_automatic_fix":true
		}]
	}`}}
	if _, err := definition.Check(context.Background(), plugin.CheckInput{Executor: stub}); err == nil {
		t.Fatal("expected unknown script-controlled capability field rejection")
	}
}

func TestDefinitionRejectsUnknownAndDuplicateItems(t *testing.T) {
	definition, err := New(metadata(), "example-health")
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		`{"schema_version":"setpoint.script-check/v1","items":[{"id":"unknown","status":"safe","current_value":"ok","evidence_summary":"x"}]}`,
		`{"schema_version":"setpoint.script-check/v1","items":[{"id":"example.service.running","status":"safe","current_value":"running","evidence_summary":"x"},{"id":"example.service.running","status":"safe","current_value":"running","evidence_summary":"x"}]}`,
	}
	for _, payload := range tests {
		stub := &stubExecutor{result: executor.Result{Stdout: payload}}
		if _, err := definition.Check(context.Background(), plugin.CheckInput{Executor: stub}); err == nil {
			t.Fatalf("expected payload rejection: %s", payload)
		}
	}
}

func TestDefinitionPreservesManualReviewAndErrorSemantics(t *testing.T) {
	definition, err := New(metadata(), "example-health")
	if err != nil {
		t.Fatal(err)
	}
	stub := &stubExecutor{result: executor.Result{Stdout: `{
		"schema_version":"setpoint.script-check/v1",
		"items":[
			{"id":"example.service.running","status":"manual_review","current_value":"unknown","evidence_summary":"two sources disagree","review_reason":"conflicting sources"},
			{"id":"example.queue.depth","status":"error","evidence_summary":"queue command failed","error":{"code":"queue_read_failed","message":"permission denied"}}
		]
	}`}}
	items, err := definition.Check(context.Background(), plugin.CheckInput{Executor: stub})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != task.ItemManualReview || items[0].ReviewReason != "conflicting sources" || items[0].Compliant != nil {
		t.Fatalf("manual item=%#v", items[0])
	}
	if items[1].Status != task.ItemError || items[1].Error == nil || items[1].Error.Code != "queue_read_failed" {
		t.Fatalf("error item=%#v", items[1])
	}
}

func TestDefinitionFailsClosedOnExecutionOrTruncation(t *testing.T) {
	definition, err := New(metadata(), "example-health")
	if err != nil {
		t.Fatal(err)
	}
	expected := errors.New("boom")
	stub := &stubExecutor{err: expected}
	if _, err := definition.Check(context.Background(), plugin.CheckInput{Executor: stub}); !errors.Is(err, expected) {
		t.Fatalf("execution err=%v", err)
	}
	stub = &stubExecutor{result: executor.Result{Stdout: `{"schema_version":"setpoint.script-check/v1","items":[]}`, StdoutTruncated: true}}
	if _, err := definition.Check(context.Background(), plugin.CheckInput{Executor: stub}); err == nil {
		t.Fatal("expected truncation rejection")
	}
}
