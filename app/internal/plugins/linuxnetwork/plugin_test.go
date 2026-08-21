package linuxnetwork

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/sysctlconfig"
	"setpoint/internal/task"
)

func TestSourceRouteUsesApprovedNonGatewayPolicy(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("sysctl", "-n", definitions[0].ID): {Stdout: "0\n"},
		key("sysctl", "-n", definitions[1].ID): {Stdout: "1\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		Parameters:       json.RawMessage("{\"host_role\":\"non_gateway\"}"),
		SelectedCheckIDs: runtimeCheckIDs(),
	})
	if err != nil || len(items) != 2 || items[0].Status != task.ItemSafe || items[1].Status != task.ItemUnsafe {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if err := task.ValidateItem(item); err != nil {
			t.Fatalf("invalid item=%#v err=%v", item, err)
		}
	}
}

func TestSourceRouteUnknownRoleRequiresManualReview(t *testing.T) {
	execution := safeExecution()
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux", SelectedCheckIDs: runtimeCheckIDs()})
	if err != nil || len(items) != len(runtimeDefinitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Status != task.ItemManualReview || item.ReviewReason == "" {
			t.Fatalf("item=%#v", item)
		}
	}
}

func TestSourceRouteGatewayIsNotApplicableWithoutObservation(t *testing.T) {
	execution := safeExecution()
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		Parameters: json.RawMessage("{\"host_role\":\"gateway\"}"),
	})
	if err != nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Status != task.ItemNotApplicable || item.Applicable {
			t.Fatalf("item=%#v", item)
		}
	}
	if len(execution.commands) != 0 {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestSourceRouteSelectionAndProbeFailureAreIsolated(t *testing.T) {
	execution := &fixtureExecutor{
		results: map[string]executor.Result{key("sysctl", "-n", definitions[0].ID): {Stdout: "0\n"}},
		errors:  map[string]error{key("sysctl", "-n", definitions[1].ID): errors.New("unselected failure")},
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		Parameters:       json.RawMessage("{\"host_role\":\"non_gateway\"}"),
		SelectedCheckIDs: []string{definitions[0].ID, definitions[0].ID},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemSafe || len(execution.commands) != 1 {
		t.Fatalf("items=%#v commands=%#v err=%v", items, execution.commands, err)
	}

	failing := &fixtureExecutor{errors: map[string]error{
		key("sysctl", "-n", definitions[0].ID): errors.New("permission denied"),
	}}
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: failing, System: "linux",
		Parameters:       json.RawMessage("{\"host_role\":\"non_gateway\"}"),
		SelectedCheckIDs: []string{definitions[0].ID},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestSourceRouteRejectsInvalidPolicyAndValue(t *testing.T) {
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: safeExecution(), System: "linux",
		Parameters:       json.RawMessage("{\"host_role\":\"routerish\"}"),
		SelectedCheckIDs: []string{definitions[0].ID},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError ||
		items[0].Error == nil || items[0].Error.Code != "invalid_check_parameters" {
		t.Fatalf("items=%#v err=%v", items, err)
	}

	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("sysctl", "-n", definitions[0].ID): {Stdout: "2\n"},
	}}
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		Parameters:       json.RawMessage("{\"host_role\":\"non_gateway\"}"),
		SelectedCheckIDs: []string{definitions[0].ID},
	})
	if err == nil || items[0].Status != task.ItemError || items[0].Error.Code != "source_route_value_invalid" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestSourceRouteMetadataCarriesSourceRefs(t *testing.T) {
	metadata := New().Metadata()
	if metadata.Version != "1.1.0" || len(metadata.Checks) != 4 || len(metadata.Parameters) != 1 {
		t.Fatalf("metadata=%#v", metadata)
	}
	for _, current := range metadata.Checks {
		if len(current.SourceRefs) != 1 || !strings.Contains(current.SourceRefs[0], "1.10") {
			t.Fatalf("source refs=%#v", current)
		}
	}
}

func TestPersistentSourceRouteCombinesRuntimeAndPersistentValues(t *testing.T) {
	now := timeForSourceRouteTest()
	definition := persistedDefinitions[0]
	resolved := sysctlconfig.Resolution{State: sysctlconfig.StateResolved, Value: "0", SourceClass: "systemd_procps_consistent", Digest: strings.Repeat("a", 64)}
	if item := persistentSourceRouteItem(definition, "non_gateway", "0", resolved, now); item.Status != task.ItemSafe {
		t.Fatalf("safe item=%#v", item)
	}
	resolved.Value = "1"
	if item := persistentSourceRouteItem(definition, "non_gateway", "0", resolved, now); item.Status != task.ItemUnsafe {
		t.Fatalf("unsafe item=%#v", item)
	}
	resolved.State, resolved.Value, resolved.Reason = sysctlconfig.StateAmbiguous, "", "views differ"
	if item := persistentSourceRouteItem(definition, "non_gateway", "0", resolved, now); item.Status != task.ItemManualReview || item.ReviewReason == "" {
		t.Fatalf("manual item=%#v", item)
	}
	if item := persistentSourceRouteItem(definition, "unknown", "1", resolved, now); item.Status != task.ItemManualReview || !strings.Contains(item.ReviewReason, "host role") {
		t.Fatalf("role item=%#v", item)
	}
}

func TestPersistentSourceRouteSelectionDoesNotReadOtherRuntimeKeys(t *testing.T) {
	selected := persistedDefinitions[0].ID
	runtimeKey := strings.TrimSuffix(selected, ".persisted")
	execution := &fixtureExecutor{results: map[string]executor.Result{key("sysctl", "-n", runtimeKey): {Stdout: "0\n"}}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", Parameters: json.RawMessage("{\"host_role\":\"non_gateway\"}"),
		SelectedCheckIDs: []string{selected, selected},
	})
	if err != nil || len(items) != 1 || items[0].ID != selected {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, command := range execution.commands {
		if command.Name == "sysctl" && (len(command.Args) != 2 || command.Args[1] != runtimeKey) {
			t.Fatalf("unselected runtime key observed: %#v", command)
		}
	}
}

func timeForSourceRouteTest() time.Time {
	return time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
}

func safeExecution() *fixtureExecutor {
	return &fixtureExecutor{results: map[string]executor.Result{
		key("sysctl", "-n", definitions[0].ID): {Stdout: "0\n"},
		key("sysctl", "-n", definitions[1].ID): {Stdout: "0\n"},
	}}
}

func runtimeCheckIDs() []string {
	return []string{runtimeDefinitions[0].ID, runtimeDefinitions[1].ID}
}

type fixtureExecutor struct {
	results  map[string]executor.Result
	errors   map[string]error
	commands []executor.Command
}

func (execution *fixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	lookup := key(command.Name, command.Args...)
	return execution.results[lookup], execution.errors[lookup]
}

func key(name string, args ...string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}
