package checkrun

import (
	"errors"
	"reflect"
	"setpoint/internal/plugins"
	"testing"
	"time"

	"setpoint/internal/plugin"
	"setpoint/internal/remediation"
	"setpoint/internal/remediationbindings"
	"setpoint/internal/task"
)

const icmpRedirectRuntimeRepairOperationID = "linux.network.icmp_redirects.runtime_repair"

func productionBindings(t *testing.T) *remediation.Registry {
	t.Helper()
	registry, err := remediationbindings.New()
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
func autoSafeRemediation() plugin.RemediationMetadata {
	return plugin.RemediationMetadata{
		Disposition: plugin.RemediationAutoSafe,
		OperationID: icmpRedirectRuntimeRepairOperationID,
		Reason:      "bounded operation",
	}
}

func TestBuildRemediationOffersUnboundFlagsRemainManualOnly(t *testing.T) {
	now := time.Now().UTC()
	unsafe := false
	run := Resource{
		Metadata: Metadata{ID: "run-1"},
		Tasks: []task.Resource{{
			Metadata: task.Metadata{ID: "task-1"},
			Spec:     task.Spec{NodeID: "node-1"},
			Result: &task.CheckResult{Items: []task.CheckItem{{
				ID: "net.ipv4.conf.all.accept_redirects", Status: task.ItemUnsafe, Name: "redirects",
				CurrentValue: "1", RecommendedValue: "0", Compliant: &unsafe,
				Risk: "medium", Remediation: "Set the controlled value to 0.", Applicable: true,
				SupportsAutomaticFix: true, SupportsRollback: true, ExecutedAt: now,
			}}},
		}},
	}
	remediations := map[string]plugin.RemediationMetadata{
		"net.ipv4.conf.all.accept_redirects": {
			Disposition: plugin.RemediationControlled,
			Reason:      "persistent state is not frozen",
		},
	}

	offers := BuildRemediationOffers(run, remediations, productionBindings(t))
	if len(offers) != 1 {
		t.Fatalf("offers=%#v", offers)
	}
	offer := offers[0]
	if offer.CheckRunID != "run-1" || offer.TaskID != "task-1" || offer.NodeID != "node-1" || offer.CheckID != "net.ipv4.conf.all.accept_redirects" {
		t.Fatalf("identity=%#v", offer)
	}
	if offer.CurrentValue != "1" || offer.ExistingRecommendedValue != "0" || offer.RecommendedValueForThisRun != "0" {
		t.Fatalf("values=%#v", offer)
	}
	if offer.RecommendationReason != "Set the controlled value to 0." {
		t.Fatalf("reason=%q", offer.RecommendationReason)
	}
	if offer.Disposition != string(plugin.RemediationControlled) || offer.Availability != "manual_only" || offer.Editable || offer.OperationID != "" || len(offer.OperationParameters) != 0 {
		t.Fatalf("classification=%#v", offer)
	}
	if offer.SupportsAutomaticFix || offer.SupportsRollback {
		t.Fatalf("unbound item flags must not become executable capability facts: %#v", offer)
	}
	if offer.BlockReason != "check remediation disposition is CONTROLLED" {
		t.Fatalf("block_reason=%q", offer.BlockReason)
	}
}

func TestBuildRemediationOffersBindsProvenICMPRedirectRepairAsFixedTarget(t *testing.T) {
	now := time.Now().UTC()
	unsafe := false
	item := task.CheckItem{
		ID: "net.ipv4.conf.all.accept_redirects.persisted", Status: task.ItemUnsafe, Name: "persisted redirects",
		CurrentValue: "runtime=1; persisted=0", RecommendedValue: "runtime=0; persisted=0", Compliant: &unsafe,
		Risk: "medium", Remediation: "Use a controlled change to set both runtime and persistent values to 0.", Applicable: true,
		ExecutedAt: now,
	}
	run := Resource{Metadata: Metadata{ID: "run-1"}, Tasks: []task.Resource{{
		Metadata: task.Metadata{ID: "task-1"}, Spec: task.Spec{NodeID: "node-1"}, Result: &task.CheckResult{Items: []task.CheckItem{item}},
	}}}
	offer := BuildRemediationOffers(run, map[string]plugin.RemediationMetadata{item.ID: autoSafeRemediation()}, productionBindings(t))[0]
	if offer.Disposition != string(plugin.RemediationAutoSafe) || offer.Availability != "actionable" || !offer.SupportsAutomaticFix || !offer.SupportsRollback {
		t.Fatalf("offer=%#v", offer)
	}
	if offer.Editable || offer.ParameterType != "string" {
		t.Fatalf("fixed target must be locked: %#v", offer)
	}
	if !reflect.DeepEqual(offer.Constraints.Options, []string{"runtime=0; persisted=0"}) {
		t.Fatalf("constraints=%#v", offer.Constraints)
	}
	if offer.OperationID != icmpRedirectRuntimeRepairOperationID {
		t.Fatalf("operation_id=%q", offer.OperationID)
	}
	if !reflect.DeepEqual(offer.OperationParameters, map[string]string{
		"check_id": item.ID, "target_value": item.RecommendedValue,
	}) {
		t.Fatalf("operation_parameters=%#v", offer.OperationParameters)
	}
}

func TestBuildRemediationOffersFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	unsafe := false
	base := task.CheckItem{
		ID: "net.ipv4.conf.all.accept_redirects.persisted", Status: task.ItemUnsafe, Name: "check",
		CurrentValue: "runtime=1; persisted=0", RecommendedValue: "runtime=0; persisted=0",
		Compliant: &unsafe, Risk: "low", Remediation: "Use the existing recommendation.", Applicable: true, ExecutedAt: now,
	}
	tests := []struct {
		name         string
		mutate       func(*task.CheckItem)
		remediations map[string]plugin.RemediationMetadata
		reason       string
	}{
		{"not_unsafe", func(item *task.CheckItem) {
			item.Status = task.ItemManualReview
			item.Compliant = nil
			item.ReviewReason = "conflicting sources"
		}, map[string]plugin.RemediationMetadata{base.ID: autoSafeRemediation()}, "check result is not an unsafe finding"},
		{"missing_current", func(item *task.CheckItem) { item.CurrentValue = "" }, map[string]plugin.RemediationMetadata{base.ID: autoSafeRemediation()}, "current value is unavailable"},
		{"missing_recommendation", func(item *task.CheckItem) { item.RecommendedValue = "" }, map[string]plugin.RemediationMetadata{base.ID: autoSafeRemediation()}, "recommended value is unavailable"},
		{"missing_metadata", func(item *task.CheckItem) {}, nil, "remediation disposition is unavailable or invalid"},
		{"controlled_metadata", func(item *task.CheckItem) {}, map[string]plugin.RemediationMetadata{base.ID: {Disposition: plugin.RemediationControlled, Reason: "approval required"}}, "check remediation disposition is CONTROLLED"},
		{"wrong_shape", func(item *task.CheckItem) { item.CurrentValue = "runtime=1; persisted=1" }, map[string]plugin.RemediationMetadata{base.ID: autoSafeRemediation()}, "no approved automatic repair capability matches this result"},
		{"connection_impact", func(item *task.CheckItem) { item.MayAffectConnection = true }, map[string]plugin.RemediationMetadata{base.ID: autoSafeRemediation()}, "connection or business impact requires manual handling"},
		{"business_impact", func(item *task.CheckItem) { item.MayAffectBusiness = true }, map[string]plugin.RemediationMetadata{base.ID: autoSafeRemediation()}, "connection or business impact requires manual handling"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := base
			test.mutate(&item)
			run := Resource{Metadata: Metadata{ID: "run-1"}, Tasks: []task.Resource{{
				Metadata: task.Metadata{ID: "task-1"}, Spec: task.Spec{NodeID: "node-1"},
				Result: &task.CheckResult{Items: []task.CheckItem{item}},
			}}}
			offers := BuildRemediationOffers(run, test.remediations, productionBindings(t))
			if len(offers) != 1 || offers[0].Availability != "manual_only" || offers[0].Editable || offers[0].BlockReason != test.reason {
				t.Fatalf("offers=%#v", offers)
			}
		})
	}
}

func TestControlledOfferUsesGenericReviewedBinding(t *testing.T) {
	item := task.CheckItem{ID: "test.controlled.setting", Status: task.ItemUnsafe, CurrentValue: "old", RecommendedValue: "new", MayAffectConnection: true, MayAffectBusiness: true, RequiresRestart: true, SupportsAutomaticFix: true}
	binding := remediation.Binding{CheckIDs: []string{item.ID}, OperationID: "test.controlled.operation", Disposition: plugin.RemediationControlled, SupportsRollback: true, Impact: remediation.Impact{Connection: true, Business: true, Restart: true}, Parameters: func(i task.CheckItem) (map[string]string, error) {
		if i.CurrentValue != "old" || i.RecommendedValue != "new" {
			return nil, errors.New("invalid shape")
		}
		return map[string]string{"setting": "new"}, nil
	}}
	registry, err := remediation.NewRegistry(binding)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]plugin.RemediationMetadata{item.ID: {Disposition: plugin.RemediationControlled, OperationID: binding.OperationID, Reason: "reviewed test-only operation"}}
	run := Resource{Metadata: Metadata{ID: "run"}, Tasks: []task.Resource{{Metadata: task.Metadata{ID: "task"}, Spec: task.Spec{NodeID: "node"}, Result: &task.CheckResult{Items: []task.CheckItem{item}}}}}
	offer := BuildRemediationOffers(run, metadata, registry)[0]
	if offer.Availability != "actionable" || offer.SupportsAutomaticFix || !offer.SupportsRollback || offer.Disposition != "CONTROLLED" || offer.Editable || offer.OperationParameters["setting"] != "new" {
		t.Fatalf("offer=%#v", offer)
	}
	for _, bindings := range []*remediation.Registry{nil, productionBindings(t)} {
		offer = BuildRemediationOffers(run, metadata, bindings)[0]
		if offer.Availability != "manual_only" || offer.SupportsAutomaticFix || offer.OperationID != "" {
			t.Fatalf("unbound=%#v", offer)
		}
	}
}

func TestProductionCatalogFourAutoSafeAndZeroControlledActionable(t *testing.T) {
	checks := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(checks); err != nil {
		t.Fatal(err)
	}
	bindings := productionBindings(t)
	counts := map[plugin.RemediationDisposition]int{}
	for _, definition := range checks.ListDefinitions() {
		item := task.CheckItem{ID: definition.ID, Status: task.ItemUnsafe, CurrentValue: "runtime=1; persisted=0", RecommendedValue: "runtime=0; persisted=0", SupportsAutomaticFix: true, SupportsRollback: true}
		run := Resource{Tasks: []task.Resource{{Result: &task.CheckResult{Items: []task.CheckItem{item}}}}}
		offer := BuildRemediationOffers(run, map[string]plugin.RemediationMetadata{item.ID: definition.Remediation}, bindings)[0]
		counts[definition.Remediation.Disposition]++
		if definition.Remediation.Disposition == plugin.RemediationAutoSafe {
			if offer.Availability != "actionable" || !offer.SupportsAutomaticFix || !offer.SupportsRollback || offer.OperationParameters["check_id"] != item.ID {
				t.Fatalf("AUTO_SAFE regression=%#v", offer)
			}
		} else if offer.Availability != "manual_only" || offer.SupportsAutomaticFix || offer.OperationID != "" {
			t.Fatalf("accidental actionability=%#v", offer)
		}
	}
	if counts[plugin.RemediationControlled] != 48 || counts[plugin.RemediationAutoSafe] != 4 {
		t.Fatalf("counts=%v", counts)
	}
}
