package linuxfiles

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestFilePermissionsReturnSafeResultsForFixedRegularPaths(t *testing.T) {
	execution := safeExecution()
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Status != task.ItemSafe {
			t.Fatalf("item=%#v", item)
		}
		if err := task.ValidateItem(item); err != nil {
			t.Fatalf("invalid item=%#v err=%v", item, err)
		}
	}
	if len(execution.commands) != 4*len(definitions) {
		t.Fatalf("commands=%d want=%d", len(execution.commands), 4*len(definitions))
	}
}

func TestFilePermissionsRejectUnsafeModeOwnerAndType(t *testing.T) {
	execution := newFixture()
	execution.results[key("stat", "-c", "%a|%U|%G", "--", "/etc/group")] = executor.Result{Stdout: "666|admin|users\n"}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"permissions.group"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemUnsafe ||
		!strings.Contains(items[0].CurrentValue, "owner=admin") {
		t.Fatalf("items=%#v err=%v", items, err)
	}

	wrongType := newFixture()
	wrongType.setFalse("test", "-f", "/etc/group")
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: wrongType, System: "linux", SelectedCheckIDs: []string{"permissions.group"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemUnsafe ||
		!strings.Contains(items[0].CurrentValue, "unexpected") {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestFilePermissionsDoNotFollowSymbolicLinks(t *testing.T) {
	execution := newFixture()
	execution.results[key("test", "-L", "/etc/group")] = executor.Result{ExitCode: 0}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"permissions.group"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemManualReview ||
		!strings.Contains(items[0].ReviewReason, "symbolic link") {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestFilePermissionsSeparateOptionalAndRequiredMissingPaths(t *testing.T) {
	optional := newFixture()
	optional.setFalse("test", "-e", "/var/log/wtmp")
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: optional, System: "linux", SelectedCheckIDs: []string{"permissions.wtmp"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemNotApplicable || items[0].Applicable {
		t.Fatalf("items=%#v err=%v", items, err)
	}

	required := newFixture()
	required.setFalse("test", "-e", "/etc/group")
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: required, System: "linux", SelectedCheckIDs: []string{"permissions.group"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemManualReview {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestFilePermissionsExposeProbeFailure(t *testing.T) {
	execution := newFixture()
	lookup := key("test", "-L", "/etc/group")
	execution.errors[lookup] = errors.New("permission denied")
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"permissions.group"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError ||
		items[0].Error == nil || items[0].Error.Code != "path_symlink_probe_failed" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestFilePermissionsWtmpPolicyIsExplicitAndIsolated(t *testing.T) {
	execution := newFixture()
	execution.results[key("stat", "-c", "%a|%U|%G", "--", "/var/log/wtmp")] = executor.Result{Stdout: "664|root|utmp\n"}
	input := plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"permissions.wtmp"},
	}
	items, err := New().Check(context.Background(), input)
	if err != nil || items[0].Status != task.ItemUnsafe {
		t.Fatalf("default items=%#v err=%v", items, err)
	}
	input.Parameters = json.RawMessage("{\"wtmp_group_write_policy\":\"allow\"}")
	items, err = New().Check(context.Background(), input)
	if err != nil || items[0].Status != task.ItemSafe {
		t.Fatalf("approved items=%#v err=%v", items, err)
	}
	input.Parameters = json.RawMessage("{\"wtmp_group_write_policy\":\"invalid\"}")
	items, err = New().Check(context.Background(), input)
	if err == nil || items[0].Status != task.ItemError {
		t.Fatalf("invalid items=%#v err=%v", items, err)
	}
}

func TestFilePermissionSelectionRunsOnlySelectedPath(t *testing.T) {
	execution := newFixture()
	execution.results[key("stat", "-c", "%a|%U|%G", "--", "/etc/group")] = executor.Result{Stdout: "644|root|root\n"}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		SelectedCheckIDs: []string{"permissions.group", "permissions.group"},
	})
	if err != nil || len(items) != 1 || items[0].ID != "permissions.group" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 4 {
		t.Fatalf("commands=%#v", execution.commands)
	}
	for _, command := range execution.commands {
		if command.Args[len(command.Args)-1] != "/etc/group" {
			t.Fatalf("unselected path observed: %#v", command)
		}
	}
}

func TestFilePermissionMetadataCarriesSourceRefs(t *testing.T) {
	metadata := New().Metadata()
	if metadata.Version != "1.0.0" || len(metadata.Checks) != 7 || len(metadata.Parameters) != 1 {
		t.Fatalf("metadata=%#v", metadata)
	}
	for _, current := range metadata.Checks {
		if len(current.SourceRefs) != 1 || current.SourceRefs[0] != "security-baseline:1.12" {
			t.Fatalf("source refs=%#v", current)
		}
	}
}

func safeExecution() *fixtureExecutor {
	execution := newFixture()
	values := map[string]string{
		"/etc/group":      "644|root|root\n",
		"/etc/gshadow":    "640|root|shadow\n",
		"/etc/services":   "644|root|root\n",
		"/etc/login.defs": "644|root|root\n",
		"/etc/security":   "755|root|root\n",
		"/var/spool/cron": "700|root|root\n",
		"/var/log/wtmp":   "644|root|utmp\n",
	}
	for path, value := range values {
		execution.results[key("stat", "-c", "%a|%U|%G", "--", path)] = executor.Result{Stdout: value}
	}
	return execution
}

type fixtureExecutor struct {
	results  map[string]executor.Result
	errors   map[string]error
	commands []executor.Command
}

func newFixture() *fixtureExecutor {
	return &fixtureExecutor{results: map[string]executor.Result{}, errors: map[string]error{}}
}

func (execution *fixtureExecutor) setFalse(name string, args ...string) {
	lookup := key(name, args...)
	result := executor.Result{ExitCode: 1}
	execution.results[lookup] = result
	execution.errors[lookup] = &executor.Error{Kind: executor.ErrorExit, Result: result, Err: errors.New("exit status 1")}
}

func (execution *fixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	lookup := key(command.Name, command.Args...)
	if err, exists := execution.errors[lookup]; exists {
		return execution.results[lookup], err
	}
	if result, exists := execution.results[lookup]; exists {
		return result, nil
	}
	if command.Name == "test" {
		if command.Args[0] == "-L" {
			result := executor.Result{ExitCode: 1}
			return result, &executor.Error{Kind: executor.ErrorExit, Result: result, Err: errors.New("exit status 1")}
		}
		return executor.Result{ExitCode: 0}, nil
	}
	return executor.Result{}, execution.errors[lookup]
}

func key(name string, args ...string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}
