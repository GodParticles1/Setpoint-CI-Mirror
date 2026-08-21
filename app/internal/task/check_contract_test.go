package task

import (
	"encoding/json"
	"testing"

	"setpoint/internal/trustedexec"
)

func TestCheckExecutionContractIsCanonicalAndDigestBound(t *testing.T) {
	contract, digest, err := NewCheckExecutionContract("linux.baseline.core", "2.0.0", json.RawMessage(`{"z":2,"a":1}`), []CheckDefinitionSnapshot{
		{ID: "shell.umask", Name: "umask", RecommendedValue: "027", SourceRefs: []string{"module:43", "docx:1.6", "module:43"}},
		{ID: "shell.tmout", Name: "TMOUT", RecommendedValue: "1-900"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(contract.Parameters) != `{"a":1,"z":2}` || contract.Checks[0].ID != "shell.tmout" ||
		len(contract.Checks[1].SourceRefs) != 2 {
		t.Fatalf("contract is not canonical: %#v", contract)
	}
	if err := ValidateCheckExecutionContract(contract, digest); err != nil {
		t.Fatalf("validate frozen contract: %v", err)
	}
	contract.PluginVersion = "2.0.1"
	if err := ValidateCheckExecutionContract(contract, digest); err == nil {
		t.Fatal("mutated contract retained a valid digest")
	}
}

func TestCheckExecutionContractFreezesTrustedRootsAndBindsThemToDigest(t *testing.T) {
	definition := CheckDefinitionSnapshot{ID: "nginx.syntax", Name: "syntax", RecommendedValue: "valid"}
	roots := []trustedexec.Root{{
		Path: "/opt/company/nginx/bin", Scope: trustedexec.ScopeNode, Source: "node:test",
	}}
	contract, digest, err := NewCheckExecutionContract(
		"nginx.baseline.core", "2.2.0", json.RawMessage(`{}`), []CheckDefinitionSnapshot{definition}, roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != 2 || len(contract.TrustedExecutableRoots) != 1 {
		t.Fatalf("contract=%#v", contract)
	}
	contract.TrustedExecutableRoots[0].Path = "/opt/unapproved/bin"
	if err := ValidateCheckExecutionContract(contract, digest); err == nil {
		t.Fatal("mutated trusted root retained a valid digest")
	}
}

func TestLegacyV1CheckExecutionContractWithoutRootsRemainsValid(t *testing.T) {
	definition := CheckDefinitionSnapshot{ID: "test.item", Name: "item", RecommendedValue: "secure"}
	contract, _, err := NewCheckExecutionContract(
		"test.check", "1", json.RawMessage(`{}`), []CheckDefinitionSnapshot{definition},
	)
	if err != nil {
		t.Fatal(err)
	}
	contract.SchemaVersion = 1
	digest, err := CheckExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCheckExecutionContract(contract, digest); err != nil {
		t.Fatalf("validate v1 contract: %v", err)
	}
}

func TestCheckExecutionContractRejectsMissingAndDuplicateChecks(t *testing.T) {
	if _, _, err := NewCheckExecutionContract("test.check", "1", json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("empty check contract was accepted")
	}
	definition := CheckDefinitionSnapshot{ID: "test.item", Name: "item", RecommendedValue: "secure"}
	if _, _, err := NewCheckExecutionContract("test.check", "1", json.RawMessage(`{}`), []CheckDefinitionSnapshot{definition, definition}); err == nil {
		t.Fatal("duplicate check contract was accepted")
	}
}
