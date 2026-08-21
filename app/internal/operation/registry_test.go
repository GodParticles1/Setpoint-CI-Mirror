package operation

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRegistrySnapshotsAndSortsMetadata(t *testing.T) {
	registry := NewRegistry()
	second := Metadata{ID: "operation.z", Category: "test", Name: "Z", Version: "1", Risk: RiskLow, SupportedSystems: []string{"linux"}}
	first := Metadata{ID: "operation.a", Category: "test", Name: "A", Version: "1", Risk: RiskLow, SupportedSystems: []string{"linux"}}
	if err := registry.Register(NewMetadataDescriptor(second)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(NewMetadataDescriptor(first)); err != nil {
		t.Fatal(err)
	}
	listed := registry.List()
	if len(listed) != 2 || listed[0].ID != "operation.a" || listed[1].ID != "operation.z" {
		t.Fatalf("unexpected list: %#v", listed)
	}
	listed[0].SupportedSystems[0] = "changed"
	got, _ := registry.Get("operation.a")
	if got.SupportedSystems[0] != "linux" {
		t.Fatal("registry metadata mutated through returned value")
	}
}

func TestRegistryFailsClosedForObjectParameterWithoutValidator(t *testing.T) {
	registry := NewRegistry()
	metadata := Metadata{ID: "operation.object", Category: "test", Name: "Object", Version: "1", Risk: RiskLow,
		SupportedSystems: []string{"linux"}, Parameters: []Parameter{{Name: "endpoint", Type: "object", Required: true, Fields: []ParameterField{{Name: "host", Type: "string", Required: true}}}}}
	if err := registry.Register(NewMetadataDescriptor(metadata)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.NormalizeParameters(metadata.ID, json.RawMessage(`{"endpoint":{"host":"node"}}`)); err == nil {
		t.Fatal("object parameter accepted without a capability validator")
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	registry := NewRegistry()
	metadata := Metadata{ID: "operation.a", Category: "test", Name: "A", Version: "1", Risk: RiskLow, SupportedSystems: []string{"linux"}}
	if err := registry.Register(NewMetadataDescriptor(metadata)); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(NewMetadataDescriptor(metadata)); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}
