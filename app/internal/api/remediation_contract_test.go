package api

import (
	"encoding/json"
	"errors"
	"setpoint/internal/remediation"
	"testing"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/plugin"
	"setpoint/internal/remediationbindings"
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

	bindings, err := remediationbindings.New()
	if err != nil {
		t.Fatal(err)
	}
	decorated := decorateCheckRun(run, definitions, bindings)
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

func TestControlledRemediationAPIContract(t *testing.T) {
	binding := remediation.Binding{CheckIDs: []string{"test.controlled"}, OperationID: "test.controlled.operation", Disposition: plugin.RemediationControlled, SupportsRollback: true, Impact: remediation.Impact{Connection: true, Business: true}, Parameters: func(item task.CheckItem) (map[string]string, error) {
		if item.CurrentValue != "old" || item.RecommendedValue != "new" {
			return nil, errors.New("invalid test shape")
		}
		return map[string]string{"setting": "new"}, nil
	}}
	registry, err := remediation.NewRegistry(binding)
	if err != nil {
		t.Fatal(err)
	}
	definitions := []plugin.CheckMetadata{{ID: "test.controlled", Remediation: plugin.RemediationMetadata{Disposition: plugin.RemediationControlled, OperationID: binding.OperationID, Reason: "test-only reviewed operation"}}}
	run := checkrun.Resource{Tasks: []task.Resource{{Result: &task.CheckResult{Items: []task.CheckItem{{ID: "test.controlled", Status: task.ItemUnsafe, CurrentValue: "old", RecommendedValue: "new", SupportsAutomaticFix: true, MayAffectConnection: true, MayAffectBusiness: true}}}}}}
	decorated := decorateCheckRun(run, definitions, registry)
	encoded, err := json.Marshal(decorated)
	if err != nil {
		t.Fatal(err)
	}
	var decoded checkrun.Resource
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	offer := decoded.RemediationOffers[0]
	if offer.Disposition != "CONTROLLED" || offer.Availability != "actionable" || offer.SupportsAutomaticFix || !offer.MayAffectConnection || !offer.MayAffectBusiness || offer.OperationID != binding.OperationID {
		t.Fatalf("JSON=%s", encoded)
	}
	unbound := decorateCheckRun(run, definitions, nil).RemediationOffers[0]
	if unbound.Availability != "manual_only" || unbound.SupportsAutomaticFix || unbound.OperationID != "" {
		t.Fatalf("unbound=%#v", unbound)
	}
}
