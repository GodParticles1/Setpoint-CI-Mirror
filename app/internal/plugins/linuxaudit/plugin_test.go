package linuxaudit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestAuditObservationReturnsStructuredResultsWithoutSensitiveValues(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("systemctl", "show", "auditd.service", "--property=LoadState", "--property=ActiveState", "--no-pager"):  {Stdout: "LoadState=loaded\nActiveState=active\n"},
		key("systemctl", "show", "rsyslog.service", "--property=LoadState", "--property=ActiveState", "--no-pager"): {Stdout: "LoadState=loaded\nActiveState=active\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/var/log/audit"):                                                       {Stdout: "750|root|root\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/var/log/audit/audit.log"):                                             {Stdout: "640|root|root\n"},
		key("cat", "--", "/etc/shadow"): {Stdout: "root:$6$private:1:2:3:4:5:6:7\nlocked:!:1:2:3:4:5:6:7\n"},
		key("cat", "--", "/etc/passwd"): {Stdout: "root:x:0:0::/root:/bin/sh\nsvc:x:100:100::/:/sbin/nologin\n"},
		key("ss", "-lntuH"):             {Stdout: "tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nudp UNCONN 0 0 0.0.0.0:68 0.0.0.0:*\n"},
		key("systemctl", "list-unit-files", "--type=service", "--state=enabled", "--no-legend", "--no-pager"): {Stdout: "auditd.service enabled\nsshd.service enabled\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if err := task.ValidateItem(item); err != nil {
			t.Fatalf("invalid item %#v: %v", item, err)
		}
		if strings.Contains(item.CurrentValue+item.EvidenceSummary, "$6$private") || strings.Contains(item.CurrentValue+item.EvidenceSummary, "root:") {
			t.Fatalf("sensitive account data escaped into result: %#v", item)
		}
	}
	if items[0].Status != task.ItemSafe || items[4].Status != task.ItemSafe || items[6].Status != task.ItemManualReview {
		t.Fatalf("unexpected conclusions=%#v", items)
	}
}

func TestAuditObservationHonorsGranularSelection(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", "/etc/passwd"): {Stdout: "a:x:100:100::/:/bin/false\nb:x:100:101::/:/bin/false\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"account.duplicate_uids", "account.duplicate_uids"},
	})
	if err != nil || len(items) != 1 || items[0].ID != "account.duplicate_uids" || items[0].Status != task.ItemUnsafe {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || execution.commands[0].Name != "cat" {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestAuditObservationDoesNotGuessAlternativeLogger(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("systemctl", "show", "rsyslog.service", "--property=LoadState", "--property=ActiveState", "--no-pager"): {
			Stdout: "LoadState=not-found\nActiveState=inactive\n",
		},
	}, errors: map[string]error{
		key("systemctl", "show", "rsyslog.service", "--property=LoadState", "--property=ActiveState", "--no-pager"): errors.New("exit status 1"),
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"audit.service.rsyslog"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemManualReview || items[0].ReviewReason == "" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestAuditLogFilePermissionsAreNotApplicableWhenCurrentLogIsAbsent(t *testing.T) {
	command := key("test", "-e", "/var/log/audit/audit.log")
	execution := &fixtureExecutor{
		results: map[string]executor.Result{command: {ExitCode: 1}},
		errors: map[string]error{command: &executor.Error{
			Kind: executor.ErrorExit, Result: executor.Result{ExitCode: 1}, Err: errors.New("exit status 1"),
		}},
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"audit.log.file_permissions"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemNotApplicable || items[0].Applicable {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if err := task.ValidateItem(items[0]); err != nil {
		t.Fatalf("invalid item %#v: %v", items[0], err)
	}
	if len(execution.commands) != 1 || execution.commands[0].Name != "test" {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestAuditLogFilePermissionsAreCheckedWhenCurrentLogExists(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("test", "-e", "/var/log/audit/audit.log"):                   {ExitCode: 0},
		key("stat", "-c", "%a|%U|%G", "--", "/var/log/audit/audit.log"): {Stdout: "640|root|root\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"audit.log.file_permissions"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemSafe {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 2 || execution.commands[0].Name != "test" || execution.commands[1].Name != "stat" {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestAuditLogFilePermissionsDoNotHidePresenceProbeFailure(t *testing.T) {
	command := key("test", "-e", "/var/log/audit/audit.log")
	execution := &fixtureExecutor{
		results: map[string]executor.Result{command: {ExitCode: -1}},
		errors: map[string]error{command: &executor.Error{
			Kind: executor.ErrorStart, Result: executor.Result{ExitCode: -1}, Err: executor.ErrCommandNotFound,
		}},
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"audit.log.file_permissions"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError || items[0].Error == nil ||
		items[0].Error.Code != string(executor.ErrorStart) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
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
