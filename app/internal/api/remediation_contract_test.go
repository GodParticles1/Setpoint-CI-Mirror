package api

import (
	"encoding/json"
	"testing"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestDecorateCheckRunExposesRemediationOfferContract(t *testing.T) {
	now := time.Now().UTC()
	unsafe := false
	checkID := "net.ipv4.conf.all.accept_redirects.persisted"
	run := checkrun.Resource{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun", Metadata: checkrun.Metadata{ID: "run-1"},
		Tasks: []task.Resource{{
			Metadata: task.Metadata{ID: "task-1"}, Spec: task.Spec{NodeID: "node-1"},
			Result: &task.CheckResult{Items: []task.CheckItem{{
				ID: checkID, Status: task.ItemUnsafe, Name: "persisted redirects",
				CurrentValue: "runtime=1; persisted=0", RecommendedValue: "runtime=0; persisted=0",
				Compliant: &unsafe, Risk: "medium", Remediation: "Apply the validated target.", Applicable: true,
				ExecutedAt: now,
			}}},
		}},
	}
	definitions := []plugin.CheckMetadata{{
		ID: checkID,
		Remediation: plugin.RemediationMetadata{
			Disposition: plugin.RemediationAutoSafe,
			OperationID: "linux.network.icmp_redirects.runtime_repair",
			Reason:      "bounded operation",
		},
	}}

	decorated := decorateCheckRun(run, definitions)
	if len(decorated.RemediationOffers) != 1 {
		t.Fatalf("offers=%#v", decorated.RemediationOffers)
	}
	if len(run.RemediationOffers) != 0 {
		t.Fatal("decorating a response must not mutate the source resource")
	}
	if decorated.RemediationOffers[0].Disposition != string(plugin.RemediationAutoSafe) {
		t.Fatalf("disposition=%q", decorated.RemediationOffers[0].Disposition)
	}
	if decorated.RemediationOffers[0].Editable {
		t.Fatal("the fixed sysctl repair target must not be editable")
	}
	encoded, err := json.Marshal(decorated)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"tasks", "remediation_offers"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("response missing %s: %s", field, encoded)
		}
	}
	var offers []map[string]json.RawMessage
	if err := json.Unmarshal(payload["remediation_offers"], &offers); err != nil || len(offers) != 1 {
		t.Fatalf("remediation_offers=%s err=%v", payload["remediation_offers"], err)
	}
	for _, field := range []string{
		"check_run_id", "task_id", "check_id", "node_id", "current_value", "existing_recommended_value",
		"recommended_value_for_this_run", "recommendation_reason", "disposition", "availability", "editable", "parameter_type",
		"constraints", "supports_automatic_fix", "supports_rollback", "risk", "requires_restart",
		"may_affect_connection", "may_affect_business", "operation_id", "operation_parameters",
	} {
		if _, ok := offers[0][field]; !ok {
			t.Fatalf("offer missing %s: %s", field, payload["remediation_offers"])
		}
	}
}
