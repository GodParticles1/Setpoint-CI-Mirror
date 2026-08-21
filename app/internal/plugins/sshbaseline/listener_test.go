package sshbaseline

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestListenerChecksSupportMultipleConfiguredPorts(t *testing.T) {
	effective := "port 22\nport 2222\n"
	listeners := sshListener(22, 100) + sshListener(2222, 100)
	items, err := listenerItems(effective, listeners, nil, timeForTest())
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Status != task.ItemSafe || strings.Contains(item.EvidenceSummary, "pid=") || strings.Contains(item.EvidenceSummary, "0.0.0.0") {
			t.Fatalf("item=%#v", item)
		}
	}
}

func TestListenerChecksSeparateMissingAndUnexpectedPorts(t *testing.T) {
	items, err := listenerItems("port 22\n", sshListener(2222, 100), nil, timeForTest())
	if err != nil {
		t.Fatal(err)
	}
	configured := findSSHItem(t, items, "ssh.listener.configured_ports_active")
	unexpected := findSSHItem(t, items, "ssh.listener.unexpected_ports")
	if configured.Status != task.ItemUnsafe || unexpected.Status != task.ItemUnsafe {
		t.Fatalf("items=%#v", items)
	}
}

func TestListenerChecksIgnoreReliablyUnrelatedListeners(t *testing.T) {
	listeners := sshListener(22, 100) + "LISTEN 0 128 127.0.0.1:8080 0.0.0.0:* users:((\"nginx\",pid=200,fd=7))\n"
	items, err := listenerItems("port 22\n", listeners, nil, timeForTest())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Status != task.ItemSafe {
			t.Fatalf("item=%#v", item)
		}
	}
}

func TestListenerChecksRequireManualReviewForAmbiguousOwnership(t *testing.T) {
	tests := map[string]string{
		"missing process":    "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n",
		"socket activation":  "LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:((\"systemd\",pid=1,fd=9))\n",
		"multiple sshd pids": sshListener(22, 100) + sshListener(22, 101),
		"unsupported row":    "unexpected output\n" + sshListener(22, 100),
	}
	for name, listeners := range tests {
		t.Run(name, func(t *testing.T) {
			items, err := listenerItems("port 22\n", listeners, nil, timeForTest())
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Status != task.ItemManualReview || item.ReviewReason == "" {
					t.Fatalf("item=%#v", item)
				}
			}
		})
	}
}

func TestListenerChecksRejectMissingOrInvalidEffectivePorts(t *testing.T) {
	for _, effective := range []string{"permitrootlogin no\n", "port invalid\n", "port 0\n", "port 22 extra\n"} {
		items, err := listenerItems(effective, "", nil, timeForTest())
		if err == nil || len(items) != 2 {
			t.Fatalf("effective=%q items=%#v err=%v", effective, items, err)
		}
		for _, item := range items {
			if item.Status != task.ItemError {
				t.Fatalf("item=%#v", item)
			}
		}
	}
}

func TestListenerSelectionRunsOnlyRequiredCommandsAndReturnsOneResult(t *testing.T) {
	selected := "ssh.listener.unexpected_ports"
	execution := &sshFixtureExecutor{results: map[string]executor.Result{
		commandKey("sshd", "-T"):        {Stdout: "port 22\n"},
		commandKey("ss", "-H", "-lntp"): {Stdout: sshListener(22, 100)},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{selected, selected},
	})
	if err != nil || len(items) != 1 || items[0].ID != selected || items[0].Status != task.ItemSafe {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 2 || execution.commands[0].Name != "sshd" || execution.commands[1].Name != "ss" {
		t.Fatalf("commands=%#v", execution.commands)
	}
	if execution.commands[0].OutputLimit != 0 || execution.commands[1].OutputLimit != executor.MaxOutputLimit {
		t.Fatalf("listener output budgets=%#v", execution.commands)
	}
}

func TestListenerCommandFailuresStayTechnicalErrors(t *testing.T) {
	execution := &sshFixtureExecutor{results: map[string]executor.Result{
		commandKey("sshd", "-T"): {Stdout: "port 22\n"},
	}, errors: map[string]error{
		commandKey("ss", "-H", "-lntp"): &executor.Error{Kind: executor.ErrorTimeout, Err: fmt.Errorf("deadline")},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"ssh.listener.configured_ports_active"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError || items[0].Error.Code != string(executor.ErrorTimeout) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestListenerObservationFailsClosedWhenMaximumBudgetStillTruncates(t *testing.T) {
	execution := &sshFixtureExecutor{results: map[string]executor.Result{
		commandKey("sshd", "-T"):        {Stdout: "port 22\n"},
		commandKey("ss", "-H", "-lntp"): {StdoutTruncated: true},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{"ssh.listener.configured_ports_active"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError || items[0].Error.Code != "ssh_listener_read_failed" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 2 || execution.commands[1].OutputLimit != executor.MaxOutputLimit {
		t.Fatalf("listener maximum budget was not explicit: %#v", execution.commands)
	}
}

func TestListenerMetadataUsesOnlyApprovedSource(t *testing.T) {
	metadata := New().Metadata()
	for _, definition := range metadata.Checks {
		if strings.HasPrefix(definition.ID, "ssh.listener.") {
			if len(definition.SourceRefs) != 1 || definition.SourceRefs[0] != "security-baseline:1.16" {
				t.Fatalf("definition=%#v", definition)
			}
		}
	}
}
