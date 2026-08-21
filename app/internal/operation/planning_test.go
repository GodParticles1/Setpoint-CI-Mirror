package operation

import (
	"encoding/json"
	"testing"
)

func TestPlanDigestBindsFrozenOperationSpecification(t *testing.T) {
	targets := []Target{{Kind: TargetNode, NodeID: "node-1"}}
	parameters := json.RawMessage(`{"mode":"safe"}`)
	plan := Plan{SchemaVersion: "test.plan.v1", Summary: "plan", Steps: []PlanStep{}, Execution: Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}
	impact := Impact{Summary: "impact", Risk: RiskLow, Changes: []Change{}}
	baseline, err := PlanDigest("sha256:capability", targets, parameters, nil, plan, impact)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		targets    []Target
		parameters json.RawMessage
		secretRefs []SecretRef
	}{
		{name: "target", targets: []Target{{Kind: TargetNode, NodeID: "node-2"}}, parameters: parameters},
		{name: "parameters", targets: targets, parameters: json.RawMessage(`{"mode":"other"}`)},
		{name: "secret refs", targets: targets, parameters: parameters, secretRefs: []SecretRef{{RequirementID: "credential", Reference: "ref-1"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			digest, err := PlanDigest("sha256:capability", testCase.targets, testCase.parameters, testCase.secretRefs, plan, impact)
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseline {
				t.Fatalf("%s did not change digest %q", testCase.name, digest)
			}
		})
	}
}

func TestPlanDigestTreatsOmittedAndEmptySecretRefsAsEquivalent(t *testing.T) {
	targets := []Target{{Kind: TargetNode, NodeID: "node-1"}}
	plan := Plan{SchemaVersion: "test.plan.v1", Summary: "plan", Steps: []PlanStep{}, Execution: Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{}`)}}
	impact := Impact{Summary: "impact", Risk: RiskLow, Changes: []Change{}}
	withoutRefs, err := PlanDigest("sha256:capability", targets, json.RawMessage(`{}`), nil, plan, impact)
	if err != nil {
		t.Fatal(err)
	}
	emptyRefs, err := PlanDigest("sha256:capability", targets, json.RawMessage(`{}`), []SecretRef{}, plan, impact)
	if err != nil {
		t.Fatal(err)
	}
	if withoutRefs != emptyRefs {
		t.Fatalf("nil and empty secret refs changed digest: %q != %q", withoutRefs, emptyRefs)
	}
}
