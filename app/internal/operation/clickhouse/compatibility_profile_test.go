package clickhouse

import (
	"strings"
	"testing"
)

func validCompatibilityEvidenceProfile() CompatibilityEvidenceProfile {
	return CompatibilityEvidenceProfile{
		SchemaVersion: compatibilityEvidenceSchemaV1,
		EvidenceID: "physical_lab:test",
		SourceVersion: "23.12.1.9",
		TargetVersion: "20.3.5.21",
		Strategy: StrategyNativeStream,
		VerifiedTypes: []string{"UInt64", "Nullable(String)"},
		UnsupportedTypeFamilies: []string{"Map", "Object"},
	}
}

func TestCompatibilityEvidenceProfileAcceptsReviewedExactProfile(t *testing.T) {
	if err := validateCompatibilityEvidenceProfile(validCompatibilityEvidenceProfile()); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityEvidenceProfileCatalogAcceptsUniqueKeys(t *testing.T) {
	first := validCompatibilityEvidenceProfile()
	second := validCompatibilityEvidenceProfile()
	second.EvidenceID = "physical_lab:other"
	second.SourceVersion = "24.1.1.1"
	second.TargetVersion = "23.12.1.9"
	if err := validateCompatibilityEvidenceProfileCatalog([]CompatibilityEvidenceProfile{first, second}); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityEvidenceProfileCatalogRejectsDuplicateExactKey(t *testing.T) {
	first := validCompatibilityEvidenceProfile()
	second := validCompatibilityEvidenceProfile()
	second.EvidenceID = "physical_lab:conflicting"
	if err := validateCompatibilityEvidenceProfileCatalog([]CompatibilityEvidenceProfile{first, second}); err == nil || !strings.Contains(err.Error(), "duplicate compatibility evidence profile") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompatibilityEvidenceProfileCatalogAllowsSameVersionsForDifferentStrategy(t *testing.T) {
	first := validCompatibilityEvidenceProfile()
	second := validCompatibilityEvidenceProfile()
	second.EvidenceID = "physical_lab:backup"
	second.Strategy = StrategyBuiltinBackupRestore
	if err := validateCompatibilityEvidenceProfileCatalog([]CompatibilityEvidenceProfile{first, second}); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityEvidenceProfileRejectsUnknownStrategy(t *testing.T) {
	profile := validCompatibilityEvidenceProfile()
	profile.Strategy = StrategyID("unknown_transport")
	if err := validateCompatibilityEvidenceProfile(profile); err == nil || !strings.Contains(err.Error(), "unknown strategy") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompatibilityEvidenceProfileRejectsEmptyUnsupportedFamily(t *testing.T) {
	profile := validCompatibilityEvidenceProfile()
	profile.UnsupportedTypeFamilies = []string{"Map", " "}
	if err := validateCompatibilityEvidenceProfile(profile); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompatibilityEvidenceProfileRejectsDuplicateUnsupportedFamily(t *testing.T) {
	profile := validCompatibilityEvidenceProfile()
	profile.UnsupportedTypeFamilies = []string{"Map", "Map"}
	if err := validateCompatibilityEvidenceProfile(profile); err == nil || !strings.Contains(err.Error(), "duplicate unsupported type family") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompatibilityEvidenceProfileRejectsVerifiedUnsupportedConflict(t *testing.T) {
	profile := validCompatibilityEvidenceProfile()
	profile.VerifiedTypes = []string{"UInt64", "Map(String,String)"}
	profile.UnsupportedTypeFamilies = []string{"Map"}
	if err := validateCompatibilityEvidenceProfile(profile); err == nil || !strings.Contains(err.Error(), "both physically verified and unsupported") {
		t.Fatalf("err=%v", err)
	}
}

func TestCompatibilityEvidenceProfileRejectsDuplicateVerifiedTypeAfterNormalization(t *testing.T) {
	profile := validCompatibilityEvidenceProfile()
	profile.VerifiedTypes = []string{"Nullable(String)", " Nullable( String ) "}
	if err := validateCompatibilityEvidenceProfile(profile); err == nil || !strings.Contains(err.Error(), "duplicate verified type") {
		t.Fatalf("err=%v", err)
	}
}
