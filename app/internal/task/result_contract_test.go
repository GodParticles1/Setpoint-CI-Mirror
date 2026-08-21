package task

import (
	"strings"
	"testing"
	"time"
)

func TestValidateCheckResultContract(t *testing.T) {
	now := time.Now().UTC()
	contract := ResultContract{PluginID: "linux.baseline", PluginVersion: "2.0.0", ItemIDs: []string{"first", "second"}}
	validItem := func(id string, status ItemStatus) CheckItem {
		item := CheckItem{ID: id, Name: id, Status: status, ExecutedAt: now}
		switch status {
		case ItemSafe:
			value := true
			item.Applicable, item.Compliant = true, &value
		case ItemError:
			item.Applicable = true
			item.Error = &Failure{Code: "read_failed", Message: "read failed"}
		case ItemNotApplicable:
			item.Applicable = false
		}
		return item
	}
	base := func() CheckResult {
		return CheckResult{
			PluginID: contract.PluginID, PluginVersion: contract.PluginVersion,
			State: CheckCompleted, StartedAt: now, CompletedAt: now,
			Items: []CheckItem{validItem("first", ItemSafe), validItem("second", ItemNotApplicable)},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*CheckResult)
		wantErr string
	}{
		{name: "normal"},
		{name: "all not applicable", mutate: func(result *CheckResult) {
			result.Items = []CheckItem{validItem("first", ItemNotApplicable), validItem("second", ItemNotApplicable)}
		}},
		{name: "missing item", mutate: func(result *CheckResult) { result.Items = result.Items[:1] }, wantErr: "missing item ID"},
		{name: "extra item", mutate: func(result *CheckResult) {
			result.Items = append(result.Items, validItem("third", ItemSafe))
		}, wantErr: "unknown item ID"},
		{name: "duplicate item", mutate: func(result *CheckResult) {
			result.Items[1] = validItem("first", ItemSafe)
		}, wantErr: "duplicate item ID"},
		{name: "wrong plugin ID", mutate: func(result *CheckResult) { result.PluginID = "ssh.baseline" }, wantErr: "plugin_id"},
		{name: "old Agent version", mutate: func(result *CheckResult) { result.PluginVersion = "1.0.0" }, wantErr: "plugin_version"},
		{name: "completed with error", mutate: func(result *CheckResult) {
			result.Error = &Failure{Code: "unexpected", Message: "unexpected"}
		}, wantErr: "must not contain"},
		{name: "partial error result", mutate: func(result *CheckResult) {
			result.State = CheckError
			result.Error = &Failure{Code: "command_failed", Message: "command failed"}
			result.Items = []CheckItem{validItem("first", ItemError)}
		}},
		{name: "unstable error", mutate: func(result *CheckResult) {
			result.State = CheckError
			result.Error = &Failure{Code: "", Message: "command failed"}
			result.Items = nil
		}, wantErr: "code and message"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := base()
			if test.mutate != nil {
				test.mutate(&result)
			}
			err := ValidateCheckResult(&result, contract)
			if test.wantErr == "" && err != nil {
				t.Fatalf("ValidateCheckResult() error = %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("ValidateCheckResult() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}
