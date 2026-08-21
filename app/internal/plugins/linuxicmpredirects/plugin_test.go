package linuxicmpredirects

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/sysctlconfig"
	"setpoint/internal/task"
)

func TestPluginReadsOnlyFixedSysctlKeys(t *testing.T) {
	execution := &recordingExecutor{values: map[string]string{
		"net.ipv4.conf.all.accept_redirects": "0\n", "net.ipv4.conf.default.accept_redirects": "1\n",
		"net.ipv4.conf.all.send_redirects": "0\n", "net.ipv4.conf.default.send_redirects": "0\n",
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux", SelectedCheckIDs: runtimeCheckIDs()})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || len(execution.commands) != 4 {
		t.Fatalf("items=%d commands=%#v", len(items), execution.commands)
	}
	for _, command := range execution.commands {
		if command.Name != "sysctl" || len(command.Args) != 2 || command.Args[0] != "-n" {
			t.Fatalf("unsafe or unexpected command: %#v", command)
		}
	}
	if items[0].Compliant == nil || !*items[0].Compliant || items[1].Compliant == nil || *items[1].Compliant {
		t.Fatalf("unexpected compliance: %#v", items)
	}
	if items[0].SupportsAutomaticFix || items[0].SupportsRollback || items[0].MayAffectConnection || items[0].MayAffectBusiness {
		t.Fatalf("read-only flags changed: %#v", items[0])
	}
}

func TestPluginReturnsStructuredPartialFailure(t *testing.T) {
	execution := &recordingExecutor{values: map[string]string{}, err: &executor.Error{Kind: executor.ErrorStart, Err: errors.New("missing sysctl")}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux", SelectedCheckIDs: runtimeCheckIDs()})
	if err == nil || len(items) != 4 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Error == nil || item.Error.Code != string(executor.ErrorStart) || item.Compliant != nil {
			t.Fatalf("unexpected failure item: %#v", item)
		}
	}
}

func TestPluginSelectionExecutesOnlySelectedSysctlOnce(t *testing.T) {
	const selected = "net.ipv4.conf.all.accept_redirects"
	execution := &recordingExecutor{values: map[string]string{selected: "0\n"}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{selected, selected},
	})
	if err != nil || len(items) != 1 || items[0].ID != selected {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || execution.commands[0].Args[1] != selected {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestPersistentRedirectCombinesRuntimeAndPersistentValues(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	definition := persistedDefinitions[0]
	resolution := sysctlconfig.Resolution{State: sysctlconfig.StateResolved, Value: "0", SourceClass: "systemd_procps_consistent", Digest: strings.Repeat("b", 64)}
	if item := persistentRedirectItem(definition, "0", resolution, now); item.Status != task.ItemSafe {
		t.Fatalf("safe item=%#v", item)
	}
	if item := persistentRedirectItem(definition, "1", resolution, now); item.Status != task.ItemUnsafe {
		t.Fatalf("runtime unsafe item=%#v", item)
	}
	resolution.Value = "1"
	if item := persistentRedirectItem(definition, "0", resolution, now); item.Status != task.ItemUnsafe {
		t.Fatalf("persisted unsafe item=%#v", item)
	}
	resolution.State, resolution.Value, resolution.Reason = sysctlconfig.StateMissing, "", "assignment missing"
	if item := persistentRedirectItem(definition, "0", resolution, now); item.Status != task.ItemManualReview || item.ReviewReason == "" {
		t.Fatalf("manual item=%#v", item)
	}
}

func TestPersistentRedirectSelectionDoesNotReadOtherRuntimeKeys(t *testing.T) {
	selected := persistedDefinitions[0].ID
	runtimeKey := strings.TrimSuffix(selected, ".persisted")
	execution := &recordingExecutor{values: map[string]string{runtimeKey: "0\n"}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux", SelectedCheckIDs: []string{selected, selected}})
	if err != nil || len(items) != 1 || items[0].ID != selected {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, command := range execution.commands {
		if command.Name == "sysctl" && (len(command.Args) != 2 || command.Args[1] != runtimeKey) {
			t.Fatalf("unselected runtime key observed: %#v", command)
		}
	}
}

type recordingExecutor struct {
	commands []executor.Command
	values   map[string]string
	err      error
}

func runtimeCheckIDs() []string {
	return []string{
		"net.ipv4.conf.all.accept_redirects",
		"net.ipv4.conf.default.accept_redirects",
		"net.ipv4.conf.all.send_redirects",
		"net.ipv4.conf.default.send_redirects",
	}
}

func (execution *recordingExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	if execution.err != nil {
		return executor.Result{}, execution.err
	}
	return executor.Result{Stdout: execution.values[command.Args[1]], ExitCode: 0}, nil
}
