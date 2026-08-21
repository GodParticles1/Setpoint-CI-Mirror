package linuxbaseline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestLinuxBaselineKeepsLimitedShellScopeForManualReview(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", "/etc/profile"):                   {Stdout: "export TMOUT=600\numask 027\nHISTSIZE=2000\n"},
		key("cat", "--", "/etc/login.defs"):                {Stdout: "PASS_MAX_DAYS 90\nPASS_MIN_DAYS 1\nPASS_MIN_LEN 8\n"},
		key("cat", "--", "/etc/issue"):                     {Stdout: "Authorized access only\n"},
		key("cat", "--", "/etc/motd"):                      {Stdout: "Authorized access only\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/etc/shadow"): {Stdout: "640|root|shadow\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/etc/passwd"): {Stdout: "644|root|root\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var safe, manual int
	for _, item := range items {
		switch item.Status {
		case task.ItemSafe:
			safe++
		case task.ItemManualReview:
			manual++
			if item.ReviewReason == "" {
				t.Fatalf("manual review item has no reason: %#v", item)
			}
		default:
			t.Fatalf("unexpected item=%#v", item)
		}
		if item.SupportsAutomaticFix || item.SupportsRollback {
			t.Fatalf("read-only item exposed mutation capability: %#v", item)
		}
	}
	if safe != 7 || manual != 3 {
		t.Fatalf("safe=%d manual=%d items=%#v", safe, manual, items)
	}
	if New().Metadata().Version != "2.2.0" {
		t.Fatalf("plugin version=%s", New().Metadata().Version)
	}
	for _, command := range execution.commands {
		if command.Name != "cat" && command.Name != "stat" && command.Name != "test" {
			t.Fatalf("unexpected command=%#v", command)
		}
	}
}

func TestLinuxBaselineDoesNotTreatMissingDefaultsAsUnsafe(t *testing.T) {
	execution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", "/etc/profile"):                   {Stdout: "# no system defaults\n"},
		key("cat", "--", "/etc/login.defs"):                {Stdout: "PASS_MAX_DAYS 99999\n"},
		key("cat", "--", "/etc/issue"):                     {Stdout: "\\S \\r\n"},
		key("cat", "--", "/etc/motd"):                      {Stdout: "Welcome\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/etc/passwd"): {Stdout: "666|root|root\n"},
	}, errors: map[string]error{
		key("stat", "-c", "%a|%U|%G", "--", "/etc/shadow"): errors.New("permission denied"),
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err == nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
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
	if unsafe != 4 || manual != 5 || failed != 1 {
		t.Fatalf("unsafe=%d manual=%d failed=%d items=%#v", unsafe, manual, failed, items)
	}
}

func TestLinuxBaselineSelectionLimitsSharedObservations(t *testing.T) {
	execution := &fixtureExecutor{
		results: map[string]executor.Result{
			key("cat", "--", "/etc/profile"): {Stdout: "export TMOUT=600\numask 027\n"},
		},
		errors: map[string]error{
			key("cat", "--", "/etc/login.defs"):                errors.New("unselected login defs failure"),
			key("cat", "--", "/etc/issue"):                     errors.New("unselected banner failure"),
			key("cat", "--", "/etc/motd"):                      errors.New("unselected motd failure"),
			key("stat", "-c", "%a|%U|%G", "--", "/etc/shadow"): errors.New("unselected shadow failure"),
			key("stat", "-c", "%a|%U|%G", "--", "/etc/passwd"): errors.New("unselected passwd failure"),
		},
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		SelectedCheckIDs: []string{"shell.tmout", "shell.umask", "shell.tmout"},
	})
	if err != nil || len(items) != 2 || items[0].ID != "shell.tmout" || items[1].ID != "shell.umask" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || key(execution.commands[0].Name, execution.commands[0].Args...) != key("cat", "--", "/etc/profile") {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestLinuxBaselineExplicitAllSelectionMatchesFullCheck(t *testing.T) {
	full, err := New().Check(context.Background(), plugin.CheckInput{Executor: completeLinuxExecution(), System: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.ID)
	}
	selected, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: completeLinuxExecution(), System: "linux", SelectedCheckIDs: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSameLinuxConclusions(t, full, selected)
}

func TestLinuxMOTDIsIndependentAndReportsProbeFailure(t *testing.T) {
	safeExecution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", "/etc/motd"): {Stdout: "Unauthorized access is prohibited\n"},
	}}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: safeExecution, System: "linux", SelectedCheckIDs: []string{"login.motd"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemSafe ||
		len(safeExecution.commands) != 2 || safeExecution.commands[1].Args[1] != "/etc/motd" {
		t.Fatalf("items=%#v err=%v commands=%#v", items, err, safeExecution.commands)
	}
	if strings.Contains(items[0].CurrentValue, "Unauthorized") {
		t.Fatalf("MOTD result exposed raw warning text: %#v", items[0])
	}
	unsafeExecution := &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", "/etc/motd"): {Stdout: "Kylin Linux\n"},
	}}
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: unsafeExecution, System: "linux", SelectedCheckIDs: []string{"login.motd"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemUnsafe {
		t.Fatalf("unsafe MOTD items=%#v err=%v", items, err)
	}
	missingExecution := &fixtureExecutor{
		results: map[string]executor.Result{
			key("test", "-e", "/etc/motd"): {ExitCode: 1},
		},
		errors: map[string]error{
			key("test", "-e", "/etc/motd"): &executor.Error{Kind: executor.ErrorExit, Err: errors.New("exit status 1")},
		},
	}
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: missingExecution, System: "linux", SelectedCheckIDs: []string{"login.motd"},
	})
	if err != nil || len(items) != 1 || items[0].Status != task.ItemUnsafe || len(missingExecution.commands) != 1 {
		t.Fatalf("missing MOTD items=%#v err=%v commands=%#v", items, err, missingExecution.commands)
	}

	failedExecution := &fixtureExecutor{errors: map[string]error{
		key("cat", "--", "/etc/motd"): errors.New("read failed"),
	}}
	items, err = New().Check(context.Background(), plugin.CheckInput{
		Executor: failedExecution, System: "linux", SelectedCheckIDs: []string{"login.motd"},
	})
	if err == nil || len(items) != 1 || items[0].Status != task.ItemError {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	metadata := New().Metadata()
	if refs := metadata.Checks[len(metadata.Checks)-1].SourceRefs; len(refs) != 1 || refs[0] != "security-baseline:1.11" {
		t.Fatalf("MOTD source refs=%#v", refs)
	}
}

func TestLinuxAccountFilePermissionsIncludeOwnerAndGroup(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		metadata fileMetadata
		shadow   bool
		want     task.ItemStatus
	}{
		{name: "shadow group", metadata: fileMetadata{Mode: "640", Owner: "root", Group: "shadow"}, shadow: true, want: task.ItemSafe},
		{name: "root group", metadata: fileMetadata{Mode: "600", Owner: "root", Group: "root"}, shadow: true, want: task.ItemSafe},
		{name: "unexpected shadow group", metadata: fileMetadata{Mode: "640", Owner: "root", Group: "users"}, shadow: true, want: task.ItemUnsafe},
		{name: "unexpected passwd owner", metadata: fileMetadata{Mode: "644", Owner: "admin", Group: "root"}, want: task.ItemUnsafe},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := definitions[8]
			if test.shadow {
				definition = definitions[7]
			}
			item := permissionItem(definition, test.metadata, nil, test.shadow, now)
			if item.Status != test.want || !strings.Contains(item.CurrentValue, "owner=") || !strings.Contains(item.CurrentValue, "group=") {
				t.Fatalf("permission item=%#v", item)
			}
		})
	}
}

func TestLinuxParsersIgnoreCommentsAndUseLastObservedValue(t *testing.T) {
	profile := parseShellProfile("# TMOUT=10\nexport TMOUT=1200\nTMOUT=600 # observed\numask 022\numask 077\nHISTSIZE=1000\n")
	if profile["shell.tmout"] != "600" || profile["shell.umask"] != "077" || profile["shell.histsize"] != "1000" {
		t.Fatalf("profile=%#v", profile)
	}
	login := parseLoginDefs("PASS_MAX_DAYS 99999\n# PASS_MAX_DAYS 1\nPASS_MAX_DAYS 60\nPASS_MIN_DAYS 2\n")
	if login["password.max_days"] != "60" || login["password.min_days"] != "2" {
		t.Fatalf("login defs=%#v", login)
	}
}

type fixtureExecutor struct {
	results  map[string]executor.Result
	errors   map[string]error
	commands []executor.Command
}

func completeLinuxExecution() *fixtureExecutor {
	return &fixtureExecutor{results: map[string]executor.Result{
		key("cat", "--", "/etc/profile"):                   {Stdout: "export TMOUT=600\numask 027\nHISTSIZE=2000\n"},
		key("cat", "--", "/etc/login.defs"):                {Stdout: "PASS_MAX_DAYS 90\nPASS_MIN_DAYS 1\nPASS_MIN_LEN 8\n"},
		key("cat", "--", "/etc/issue"):                     {Stdout: "Authorized access only\n"},
		key("cat", "--", "/etc/motd"):                      {Stdout: "Authorized access only\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/etc/shadow"): {Stdout: "640|root|shadow\n"},
		key("stat", "-c", "%a|%U|%G", "--", "/etc/passwd"): {Stdout: "644|root|root\n"},
	}}
}

func assertSameLinuxConclusions(t *testing.T, left, right []task.CheckItem) {
	t.Helper()
	if len(left) != len(right) {
		t.Fatalf("item counts differ: %d != %d", len(left), len(right))
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Status != right[index].Status ||
			left[index].CurrentValue != right[index].CurrentValue {
			t.Fatalf("item %d differs: %#v != %#v", index, left[index], right[index])
		}
	}
}

func (execution *fixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	lookup := key(command.Name, command.Args...)
	if err := execution.errors[lookup]; err != nil {
		return execution.results[lookup], err
	}
	return execution.results[lookup], nil
}

func key(name string, args ...string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}
