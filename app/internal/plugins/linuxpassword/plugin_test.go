package linuxpassword

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

func TestPasswordPolicyUsesOneObservationPerSourceAndKeepsPAMConclusionManual(t *testing.T) {
	execution := completeExecution()
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	for _, item := range items[:5] {
		if item.Status != task.ItemManualReview || item.ReviewReason == "" ||
			!strings.Contains(item.EvidenceSummary, "meets target") {
			t.Fatalf("pwquality conclusion=%#v", item)
		}
	}
	if items[5].Status != task.ItemSafe {
		t.Fatalf("warn item=%#v", items[5])
	}
	if len(execution.commands) != 2 {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestPasswordPolicyReportsNoncompliantObservationWithoutFalseEffectiveClaim(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", pwqualityPath): {Stdout: "minlen = 7\ndcredit = 0\nucredit = 0\nlcredit = 0\nocredit = 0\n"},
		key("cat", "--", loginDefsPath): {Stdout: "PASS_WARN_AGE 2\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items[:5] {
		if item.Status != task.ItemManualReview || !strings.Contains(item.EvidenceSummary, "does not meet target") {
			t.Fatalf("pwquality conclusion=%#v", item)
		}
	}
	if items[5].Status != task.ItemUnsafe {
		t.Fatalf("warn item=%#v", items[5])
	}
}

func TestPasswordPolicyHonorsTargetsAndMissingDirectiveBoundary(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", pwqualityPath): {Stdout: "minlen=12\n"},
		key("cat", "--", loginDefsPath): {Stdout: "PASS_WARN_AGE 10\n"},
	}}
	parameters := json.RawMessage("{\"pwquality_min_length_target\":12,\"password_warn_days_target\":10}")
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", Parameters: parameters,
		SelectedCheckIDs: []string{"password.pwquality.min_length", "password.pwquality.digit_credit", "password.warn_days"},
	})
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if items[0].Status != task.ItemManualReview || items[1].Status != task.ItemManualReview ||
		items[2].Status != task.ItemSafe || !strings.Contains(items[1].ReviewReason, "absent") {
		t.Fatalf("items=%#v", items)
	}
}

func TestPasswordPolicySelectionDoesNotRunUnselectedObservation(t *testing.T) {
	execution := &fixtureExecutor{
		results: map[string]executor.Result{key("cat", "--", loginDefsPath): {Stdout: "PASS_WARN_AGE 7\n"}},
		errors:  map[string]error{key("cat", "--", pwqualityPath): errors.New("unselected source failure")},
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		Parameters:       json.RawMessage("{\"pwquality_min_length_target\":\"invalid\"}"),
		SelectedCheckIDs: []string{"password.warn_days", "password.warn_days"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemSafe {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || execution.commands[0].Args[1] != loginDefsPath {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestPasswordPolicyRejectsBadTargetAndProbeFailure(t *testing.T) {
	execution := completeExecution()
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		Parameters:       json.RawMessage("{\"password_warn_days_target\":0}"),
		SelectedCheckIDs: []string{"password.warn_days"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError ||
		items[0].Error == nil || items[0].Error.Code != "invalid_check_parameters" {
		t.Fatalf("items=%#v err=%v", items, err)
	}

	failing := &fixtureExecutor{errors: map[string]error{
		key("cat", "--", pwqualityPath): errors.New("permission denied"),
	}}
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: failing, System: "linux",
		SelectedCheckIDs: []string{"password.pwquality.min_length"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestPasswordPolicyMetadataAndParsers(t *testing.T) {
	metadata := New().Metadata()
	if metadata.Version != "1.0.0" || len(metadata.Checks) != 6 || len(metadata.Parameters) != 6 {
		t.Fatalf("metadata=%#v", metadata)
	}
	for _, current := range metadata.Checks {
		if len(current.SourceRefs) != 1 {
			t.Fatalf("missing source ref=%#v", current)
		}
	}
	values := parseAssignments("# minlen = 1\nminlen = 8 # active\ndcredit -2\n")
	if values["minlen"] != "8" || values["dcredit"] != "-2" {
		t.Fatalf("values=%#v", values)
	}
	login := parseLoginDefs("PASS_WARN_AGE 5\nPASS_WARN_AGE 9 # final\n")
	if login["PASS_WARN_AGE"] != "9" {
		t.Fatalf("login=%#v", login)
	}
}

func completeExecution() *fixtureExecutor {
	return &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", pwqualityPath): {Stdout: "minlen = 8\ndcredit = -3\nucredit = -1\nlcredit = -1\nocredit = -1\n"},
		key("cat", "--", loginDefsPath): {Stdout: "PASS_WARN_AGE 7\n"},
	}}
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
