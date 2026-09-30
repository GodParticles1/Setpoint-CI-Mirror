package remediation

import (
	"errors"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
	"testing"
)

func controlledBinding() Binding {
	return Binding{CheckIDs: []string{"test.setting"}, OperationID: "test.controlled", Disposition: plugin.RemediationControlled, SupportsRollback: true,
		Parameters: func(item task.CheckItem) (map[string]string, error) {
			if item.CurrentValue != "old" || item.RecommendedValue != "new" {
				return nil, errors.New("unexpected setting shape")
			}
			return map[string]string{"setting": "new"}, nil
		}}
}

func TestRegistryRejectsInvalidAndAmbiguousDeclarations(t *testing.T) {
	tests := map[string]func(*Binding){
		"no_checks":         func(b *Binding) { b.CheckIDs = nil },
		"empty_check":       func(b *Binding) { b.CheckIDs = []string{""} },
		"untrimmed_check":   func(b *Binding) { b.CheckIDs = []string{" test.setting"} },
		"duplicate_check":   func(b *Binding) { b.CheckIDs = []string{"test.setting", "test.setting"} },
		"no_operation":      func(b *Binding) { b.OperationID = "" },
		"invalid_operation": func(b *Binding) { b.OperationID = "bad id" },
		"manual":            func(b *Binding) { b.Disposition = plugin.RemediationManualOnly },
		"no_validator":      func(b *Binding) { b.Parameters = nil },
		"no_rollback":       func(b *Binding) { b.SupportsRollback = false },
		"auto_impact":       func(b *Binding) { b.Disposition = plugin.RemediationAutoSafe; b.Impact.Connection = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			b := controlledBinding()
			mutate(&b)
			if _, err := NewRegistry(b); err == nil {
				t.Fatal("accepted invalid declaration")
			}
		})
	}
	if _, err := NewRegistry(controlledBinding(), controlledBinding()); err == nil {
		t.Fatal("accepted duplicate ownership")
	}
}

func TestControlledBindingFailsClosedAndAcceptsOnlyReviewedImpact(t *testing.T) {
	base := task.CheckItem{ID: "test.setting", Status: task.ItemUnsafe, CurrentValue: "old", RecommendedValue: "new", SupportsAutomaticFix: true, SupportsRollback: true}
	metadata := plugin.RemediationMetadata{Disposition: plugin.RemediationControlled, OperationID: "test.controlled", Reason: "reviewed"}
	tests := []struct {
		name   string
		impact Impact
		mutate func(*task.CheckItem, *plugin.RemediationMetadata)
		valid  bool
	}{
		{"controlled", Impact{}, func(*task.CheckItem, *plugin.RemediationMetadata) {}, true},
		{"connection_unreviewed", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.MayAffectConnection = true }, false},
		{"business_unreviewed", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.MayAffectBusiness = true }, false},
		{"restart_unreviewed", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.RequiresRestart = true }, false},
		{"connection_reviewed", Impact{Connection: true}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.MayAffectConnection = true }, true},
		{"business_reviewed", Impact{Business: true}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.MayAffectBusiness = true }, true},
		{"restart_reviewed", Impact{Restart: true}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.RequiresRestart = true }, true},
		{"all_reviewed", Impact{Connection: true, Business: true, Restart: true}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) {
			i.MayAffectConnection = true
			i.MayAffectBusiness = true
			i.RequiresRestart = true
		}, true},
		{"wrong_check", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.ID = "test.other" }, false},
		{"wrong_operation", Impact{}, func(_ *task.CheckItem, m *plugin.RemediationMetadata) { m.OperationID = "other.operation" }, false},
		{"missing_operation", Impact{}, func(_ *task.CheckItem, m *plugin.RemediationMetadata) { m.OperationID = "" }, false},
		{"wrong_disposition", Impact{}, func(_ *task.CheckItem, m *plugin.RemediationMetadata) { m.Disposition = plugin.RemediationAutoSafe }, false},
		{"wrong_current", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.CurrentValue = "unknown" }, false},
		{"wrong_recommendation", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.RecommendedValue = "other" }, false},
		{"safe", Impact{}, func(i *task.CheckItem, _ *plugin.RemediationMetadata) { i.Status = task.ItemSafe }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := controlledBinding()
			b.Impact = tt.impact
			registry, err := NewRegistry(b)
			if err != nil {
				t.Fatal(err)
			}
			item, meta := base, metadata
			tt.mutate(&item, &meta)
			capability, err := registry.Resolve(item, meta)
			if (err == nil) != tt.valid {
				t.Fatalf("capability=%#v err=%v", capability, err)
			}
			if capability.SupportsAutomaticFix {
				t.Fatal("CONTROLLED inherited untrusted automatic fix claim")
			}
			if tt.valid && (!capability.SupportsRollback || capability.Parameters["setting"] != "new") {
				t.Fatalf("capability=%#v", capability)
			}
			if !tt.valid && (capability.OperationID != "" || capability.Parameters != nil) {
				t.Fatal("failed binding exposed execution parameters")
			}
		})
	}
	var absent *Registry
	if _, err := absent.Resolve(base, metadata); err == nil {
		t.Fatal("nil registry accepted")
	}
	b := controlledBinding()
	b.Parameters = func(task.CheckItem) (map[string]string, error) { return nil, nil }
	registry, _ := NewRegistry(b)
	if _, err := registry.Resolve(base, metadata); err == nil {
		t.Fatal("empty parameters accepted")
	}
}

func TestRegistryDoesNotRetainMutableDeclarationOrParameterMaps(t *testing.T) {
	b := controlledBinding()
	params := map[string]string{"setting": "new"}
	b.Parameters = func(task.CheckItem) (map[string]string, error) { return params, nil }
	registry, err := NewRegistry(b)
	if err != nil {
		t.Fatal(err)
	}
	b.CheckIDs[0] = "changed"
	b.OperationID = "changed"
	item := task.CheckItem{ID: "test.setting", Status: task.ItemUnsafe, CurrentValue: "old", RecommendedValue: "new"}
	metadata := plugin.RemediationMetadata{Disposition: plugin.RemediationControlled, OperationID: "test.controlled", Reason: "reviewed"}
	first, err := registry.Resolve(item, metadata)
	if err != nil {
		t.Fatal(err)
	}
	first.Parameters["setting"] = "tampered"
	second, err := registry.Resolve(item, metadata)
	if err != nil || second.Parameters["setting"] != "new" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}
