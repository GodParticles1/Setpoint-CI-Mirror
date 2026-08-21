package plugin

import (
	"errors"
	"testing"
)

func TestRegistryBuildsGranularDefinitionsAndBundleSnapshots(t *testing.T) {
	metadata := testMetadata("system.info")
	metadata.Checks = append(metadata.Checks, CheckItemDefinition{
		ID: "second.setting", Name: "Second", RecommendedValue: "secure", SourceRefs: []string{"module:46"},
	})
	registry := NewCheckRegistry()
	if err := registry.Register(NewMetadataDescriptor(metadata)); err != nil {
		t.Fatal(err)
	}
	definitions := registry.ListDefinitions()
	bundles := registry.ListBundles()
	if len(definitions) != 2 || definitions[1].ID != "second.setting" || definitions[1].PluginID != metadata.ID ||
		len(bundles) != 1 || len(bundles[0].CheckIDs) != 2 {
		t.Fatalf("definitions=%#v bundles=%#v", definitions, bundles)
	}
	definitions[1].SourceRefs[0] = "changed"
	bundles[0].CheckIDs[0] = "changed"
	again, _ := registry.GetDefinition("second.setting")
	bundle, _ := registry.GetBundle(metadata.ID)
	if again.SourceRefs[0] != "module:46" || bundle.CheckIDs[0] == "changed" {
		t.Fatal("catalog returned mutable internal state")
	}
}

func TestRegistryRejectsDuplicateGranularCheckWithoutPartialRegistration(t *testing.T) {
	registry := NewCheckRegistry()
	if err := registry.Register(NewMetadataDescriptor(testMetadata("first.bundle"))); err != nil {
		t.Fatal(err)
	}
	second := testMetadata("second.bundle")
	err := registry.Register(NewMetadataDescriptor(second))
	if !errors.Is(err, ErrDuplicateDefinition) {
		t.Fatalf("duplicate definition error=%v", err)
	}
	if _, exists := registry.Get(second.ID); exists {
		t.Fatal("plugin was partially registered after a definition conflict")
	}
}

func TestRegistryResolvesGranularLegacyBundleAndPolicySelections(t *testing.T) {
	metadata := testMetadata("system.bundle")
	metadata.Checks = append(metadata.Checks, CheckItemDefinition{ID: "second.setting", Name: "Second", RecommendedValue: "secure"})
	registry := NewCheckRegistry()
	if err := registry.Register(NewMetadataDescriptor(metadata)); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterPolicy(CheckPolicy{
		ID: "policy.system", Name: "System", CheckIDs: []string{"second.setting"}, BundleIDs: []string{metadata.ID},
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.ResolveSelection([]string{"kernel.setting", metadata.ID}, nil, []string{"policy.system"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.CheckIDs) != 2 || len(resolved.Groups) != 1 || len(resolved.Groups[0].CheckIDs) != 2 {
		t.Fatalf("resolved=%#v", resolved)
	}
}
