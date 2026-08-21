package sshbaseline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestSSHBaselineMarksDefaultContextAsManualReview(t *testing.T) {
	execution := &sshFixtureExecutor{results: map[string]executor.Result{
		commandKey("sshd", "-T"):        {Stdout: safeSSHConfig("no")},
		commandKey("sshd", "-t"):        {},
		commandKey("ss", "-H", "-lntp"): {Stdout: sshListener(22, 100)},
		commandKey("stat", "-c", "%a|%U|%G", "--", "/etc/ssh/sshd_config"): {Stdout: "600|root|root\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil || len(items) != len(directiveDefinitions)+2+len(listenerDefinitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var safe, manual int
	for _, item := range items {
		switch item.Status {
		case task.ItemSafe:
			safe++
		case task.ItemManualReview:
			manual++
			if !strings.Contains(item.ReviewReason, "without -C") || !strings.Contains(item.EvidenceSummary, "Include") {
				t.Fatalf("scope is not explicit: %#v", item)
			}
		default:
			t.Fatalf("unexpected item=%#v", item)
		}
	}
	if safe != 4 || manual != len(directiveDefinitions) {
		t.Fatalf("safe=%d manual=%d items=%#v", safe, manual, items)
	}
	metadata := New().Metadata()
	if metadata.Version != "2.2.0" || len(metadata.Parameters) != 2 || len(metadata.Checks) != 14 {
		t.Fatalf("metadata=%#v", metadata)
	}
	for _, command := range execution.commands {
		if command.Name != "sshd" && command.Name != "stat" && command.Name != "ss" {
			t.Fatalf("unexpected command=%#v", command)
		}
	}
}

func TestSSHBaselineSeparatesUnsafeManualAndExecutionErrors(t *testing.T) {
	config := strings.Join([]string{
		"permitemptypasswords yes", "permitrootlogin yes", "maxauthtries 6", "x11forwarding yes",
		"clientaliveinterval 0", "clientalivecountmax 5", "passwordauthentication yes", "port 22",
		"banner none", "ciphers aes128-cbc,3des-cbc",
	}, "\n")
	execution := &sshFixtureExecutor{results: map[string]executor.Result{
		commandKey("sshd", "-T"):                                           {Stdout: config},
		commandKey("ss", "-H", "-lntp"):                                    {Stdout: sshListener(22, 100)},
		commandKey("stat", "-c", "%a|%U|%G", "--", "/etc/ssh/sshd_config"): {Stdout: "666|root|root\n"},
	}, errors: map[string]error{
		commandKey("sshd", "-t"): errors.New("bad configuration"),
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err == nil {
		t.Fatal("syntax failure was not propagated")
	}
	var unsafe, manual, failed int
	for _, item := range items {
		switch item.Status {
		case task.ItemUnsafe:
			unsafe++
		case task.ItemManualReview:
			manual++
		case task.ItemError:
			failed++
		}
	}
	if unsafe != 10 || manual != 1 || failed != 1 {
		t.Fatalf("unsafe=%d manual=%d failed=%d items=%#v", unsafe, manual, failed, items)
	}
}

func TestSSHBaselineUsesApprovedParameterTargets(t *testing.T) {
	execution := &sshFixtureExecutor{results: map[string]executor.Result{
		commandKey("sshd", "-T"):        {Stdout: safeSSHConfig("yes")},
		commandKey("sshd", "-t"):        {},
		commandKey("ss", "-H", "-lntp"): {Stdout: sshListener(22, 100)},
		commandKey("stat", "-c", "%a|%U|%G", "--", "/etc/ssh/sshd_config"): {Stdout: "600|root|root\n"},
	}}
	parameters := json.RawMessage(`{"password_authentication_target":"yes"}`)
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux", Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	item := findSSHItem(t, items, "ssh.password_authentication")
	if item.Status != task.ItemManualReview || item.Compliant != nil {
		t.Fatalf("parameterized item=%#v", item)
	}
	if _, err := parsePolicy(json.RawMessage(`{"permit_root_login_target":"invalid"}`)); err == nil {
		t.Fatal("invalid PermitRootLogin target was accepted")
	}
}

func TestSSHBaselineSelectionLimitsObservations(t *testing.T) {
	execution := &sshFixtureExecutor{
		results: map[string]executor.Result{
			commandKey("sshd", "-T"): {Stdout: safeSSHConfig("no")},
		},
		errors: map[string]error{
			commandKey("sshd", "-t"): errors.New("unselected syntax failure"),
			commandKey("stat", "-c", "%a|%U|%G", "--", "/etc/ssh/sshd_config"): errors.New("unselected permission failure"),
		},
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		SelectedCheckIDs: []string{"ssh.permit_root_login", "ssh.password_authentication", "ssh.permit_root_login"},
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || commandKey(execution.commands[0].Name, execution.commands[0].Args...) != commandKey("sshd", "-T") {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestSSHUnselectedParametersDoNotContaminateSyntaxCheck(t *testing.T) {
	execution := &sshFixtureExecutor{results: map[string]executor.Result{commandKey("sshd", "-t"): {}}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", Parameters: json.RawMessage("{\"permit_root_login_target\":\"invalid\"}"),
		SelectedCheckIDs: []string{"ssh.syntax"},
	})
	if err != nil || len(items) != 1 || items[0].ID != "ssh.syntax" || items[0].Status != task.ItemSafe {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || execution.commands[0].Args[0] != "-t" {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestSSHConfigPermissionsRequireRootOwnerAndGroup(t *testing.T) {
	item := sshConfigPermissionItem("600|admin|root", timeForTest())
	if item.Status != task.ItemUnsafe || !strings.Contains(item.CurrentValue, "owner=admin") {
		t.Fatalf("permission item=%#v", item)
	}
	item = sshConfigPermissionItem("640|root|root", timeForTest())
	if item.Status != task.ItemUnsafe {
		t.Fatalf("group-readable config was accepted: %#v", item)
	}
}

func TestSSHDetectReturnsNotApplicableOnlyWhenBinaryIsAbsent(t *testing.T) {
	missing := &sshFixtureExecutor{errors: map[string]error{
		commandKey("sshd", "-T"): &executor.Error{Kind: executor.ErrorStart, Err: fmt.Errorf("%w: sshd", executor.ErrCommandNotFound)},
	}}
	detection, err := New().Detect(context.Background(), plugin.CheckInput{Executor: missing, System: "linux"})
	if err != nil || detection.Applicable {
		t.Fatalf("detection=%#v err=%v", detection, err)
	}
	failing := &sshFixtureExecutor{errors: map[string]error{commandKey("sshd", "-T"): errors.New("permission denied")}}
	detection, err = New().Detect(context.Background(), plugin.CheckInput{Executor: failing, System: "linux"})
	if err == nil || !detection.Applicable {
		t.Fatalf("detection=%#v err=%v", detection, err)
	}
}

func TestEffectiveConfigParserUsesLastReportedValue(t *testing.T) {
	values := parseEffectiveConfig("permitrootlogin yes\npermitrootlogin no\nclientaliveinterval 120\n")
	if values["permitrootlogin"] != "no" || values["clientaliveinterval"] != "120" {
		t.Fatalf("values=%#v", values)
	}
}

func safeSSHConfig(passwordAuthentication string) string {
	return strings.Join([]string{
		"permitemptypasswords no", "permitrootlogin no", "maxauthtries 4", "x11forwarding no",
		"clientaliveinterval 300", "clientalivecountmax 3", "passwordauthentication " + passwordAuthentication,
		"port 22", "banner /etc/issue.net", "ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com",
	}, "\n")
}

func sshListener(port, pid int) string {
	return fmt.Sprintf("LISTEN 0 128 0.0.0.0:%d 0.0.0.0:* users:((\"sshd\",pid=%d,fd=3))\n", port, pid)
}

func findSSHItem(t *testing.T, items []task.CheckItem, id string) task.CheckItem {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("item %s not found", id)
	return task.CheckItem{}
}

func timeForTest() time.Time {
	return time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
}

type sshFixtureExecutor struct {
	results  map[string]executor.Result
	errors   map[string]error
	commands []executor.Command
}

func (execution *sshFixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	lookup := commandKey(command.Name, command.Args...)
	if err := execution.errors[lookup]; err != nil {
		return execution.results[lookup], err
	}
	return execution.results[lookup], nil
}

func commandKey(name string, args ...string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}
