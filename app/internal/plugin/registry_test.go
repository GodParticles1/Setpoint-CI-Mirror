package plugin

import (
	"errors"
	"testing"
)

func TestRegistrySnapshotsPluginMetadataAtRegistration(t *testing.T) {
	candidate := &mutablePlugin{metadata: testMetadata("system.info")}
	registry := NewCheckRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	candidate.metadata.ID = "changed.id"
	candidate.metadata.Mode = Mode("mutation")
	candidate.metadata.Risk = RiskCritical
	candidate.metadata.SupportedSystems[0] = "changed"
	candidate.metadata.Parameters[0].Options[0] = "changed"

	metadata, found := registry.Get("system.info")
	if !found {
		t.Fatal("registered snapshot not found")
	}
	if metadata.ID != "system.info" || metadata.Mode != ModeReadOnly || metadata.Risk != RiskLow ||
		metadata.SupportedSystems[0] != "linux" || metadata.Parameters[0].Options[0] != "basic" {
		t.Fatalf("registered metadata changed with plugin: %#v", metadata)
	}
	if _, found := registry.Get("changed.id"); found {
		t.Fatal("mutated plugin ID changed registry key")
	}
}

func TestRegistryReturnsDeepCopies(t *testing.T) {
	registry := NewCheckRegistry()
	if err := registry.Register(NewMetadataDescriptor(testMetadata("system.info"))); err != nil {
		t.Fatalf("register plugin: %v", err)
	}

	metadata, _ := registry.Get("system.info")
	metadata.SupportedSystems[0] = "changed"
	metadata.Parameters[0].Name = "changed"
	metadata.Parameters[0].Options[0] = "changed"
	listed := registry.List()
	listed[0].SupportedSystems[0] = "list-changed"
	listed[0].Parameters[0].Options[0] = "list-changed"

	again, _ := registry.Get("system.info")
	if again.SupportedSystems[0] != "linux" || again.Parameters[0].Name != "detail" || again.Parameters[0].Options[0] != "basic" {
		t.Fatalf("registry metadata was mutated through return values: %#v", again)
	}
}

func TestRegistryDoesNotRecallPluginMetadata(t *testing.T) {
	candidate := &countingPlugin{metadata: testMetadata("system.info")}
	registry := NewCheckRegistry()
	if err := registry.Register(candidate); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	registry.Get("system.info")
	registry.List()
	if candidate.calls != 1 {
		t.Fatalf("Metadata calls=%d, want 1", candidate.calls)
	}
}

func TestRegistryRejectsDuplicateID(t *testing.T) {
	registry := NewCheckRegistry()
	if err := registry.Register(NewMetadataDescriptor(testMetadata("system.info"))); err != nil {
		t.Fatalf("register first plugin: %v", err)
	}
	err := registry.Register(NewMetadataDescriptor(testMetadata("system.info")))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestRegistryRejectsInvalidMetadata(t *testing.T) {
	metadata := testMetadata("Invalid ID")
	err := NewCheckRegistry().Register(NewMetadataDescriptor(metadata))
	if err == nil {
		t.Fatal("invalid plugin ID was accepted")
	}
}

func TestRegistryRejectsMalformedMetadataCollections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Metadata)
	}{
		{
			name: "empty supported system",
			mutate: func(metadata *Metadata) {
				metadata.SupportedSystems = []string{"linux", "  "}
			},
		},
		{
			name: "supported system surrounding whitespace",
			mutate: func(metadata *Metadata) {
				metadata.SupportedSystems = []string{" linux "}
			},
		},
		{
			name: "duplicate supported system",
			mutate: func(metadata *Metadata) {
				metadata.SupportedSystems = []string{"linux", "LINUX"}
			},
		},
		{
			name: "parameter name surrounding whitespace",
			mutate: func(metadata *Metadata) {
				metadata.Parameters[0].Name = " detail "
			},
		},
		{
			name: "empty parameter type",
			mutate: func(metadata *Metadata) {
				metadata.Parameters[0].Type = "  "
			},
		},
		{
			name: "parameter type surrounding whitespace",
			mutate: func(metadata *Metadata) {
				metadata.Parameters[0].Type = " string "
			},
		},
		{
			name: "empty parameter option",
			mutate: func(metadata *Metadata) {
				metadata.Parameters[0].Options = []string{"basic", "  "}
			},
		},
		{
			name: "duplicate parameter option",
			mutate: func(metadata *Metadata) {
				metadata.Parameters[0].Options = []string{"basic", "basic"}
			},
		},
		{
			name: "empty source reference",
			mutate: func(metadata *Metadata) {
				metadata.Checks[0].SourceRefs = []string{"source:a", "  "}
			},
		},
		{
			name: "duplicate source reference",
			mutate: func(metadata *Metadata) {
				metadata.Checks[0].SourceRefs = []string{"source:a", "source:a"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := testMetadata("system.info")
			test.mutate(&metadata)
			if err := NewCheckRegistry().Register(NewMetadataDescriptor(metadata)); err == nil {
				t.Fatalf("malformed metadata was accepted: %#v", metadata)
			}
		})
	}
}

type mutablePlugin struct {
	metadata Metadata
}

func (plugin *mutablePlugin) Metadata() Metadata {
	return plugin.metadata
}

type countingPlugin struct {
	metadata Metadata
	calls    int
}

func (plugin *countingPlugin) Metadata() Metadata {
	plugin.calls++
	return plugin.metadata
}

func testMetadata(id string) Metadata {
	return Metadata{
		ID: id, Category: "test", Name: "System info", Version: "1.0.0", Description: "metadata test",
		Mode: ModeReadOnly, Risk: RiskLow, Impact: "none",
		SupportedSystems: []string{"linux"},
		Parameters:       []Parameter{{Name: "detail", Type: "string", Options: []string{"basic"}}},
		Checks:           []CheckItemDefinition{{ID: "kernel.setting", Name: "Kernel setting", RecommendedValue: "secure"}},
	}
}
