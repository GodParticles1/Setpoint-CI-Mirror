package clickhouse

import "testing"

func exact2312To2003Envelope(t *testing.T) TypeEnvelope {
	t.Helper()
	profile, ok, err := LoadCompatibilityEvidenceProfile("23.12.1.9", "20.3.5.21", StrategyNativeStream)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected exact physical compatibility evidence profile")
	}
	return profile.TypeEnvelope()
}

func TestVerifiedEnvelopeAllowsPhysicallyProvenExactTypes(t *testing.T) {
	report := EvaluateTypesAgainstEnvelope([]string{
		"UInt8", "UInt16", "UInt32", "UInt64",
		"Int8", "Int16", "Int32", "Int64",
		"Float32", "Float64", "String", "FixedString(16)",
		"Date", "DateTime", "UUID", "Decimal(18,4)",
		"Nullable(String)", "Array(UInt64)",
		"Enum8('zero'=0,'one'=1)", "LowCardinality(String)",
	}, exact2312To2003Envelope(t))
	if !report.Compatible {
		t.Fatalf("report=%#v", report)
	}
	if len(report.Findings) != 20 {
		t.Fatalf("findings=%d want=20", len(report.Findings))
	}
	for _, finding := range report.Findings {
		if finding.State != TypeCompatibilityVerified {
			t.Fatalf("finding=%#v", finding)
		}
	}
}

func TestVerifiedEnvelopeBlocksUnverifiedTypesAndFeatures(t *testing.T) {
	report := EvaluateTypesAgainstEnvelope([]string{
		"DateTime64(3)",
		"Array(String)",
		"Nullable(UInt64)",
		"Decimal(38,8)",
		"AggregateFunction(sum,UInt64)",
	}, exact2312To2003Envelope(t))
	if report.Compatible {
		t.Fatalf("report=%#v", report)
	}
	if len(report.Findings) != 5 {
		t.Fatalf("findings=%v", report.Findings)
	}
	for _, finding := range report.Findings {
		if finding.State != TypeCompatibilityUnknown {
			t.Fatalf("finding=%#v", finding)
		}
	}
}

func TestVerifiedEnvelopeFailsClosedForPhysicallySourceOnlyFamilies(t *testing.T) {
	profile, ok, err := LoadCompatibilityEvidenceProfile("23.12.1.9", "20.3.5.21", StrategyNativeStream)
	if err != nil || !ok {
		t.Fatalf("profile ok=%v err=%v", ok, err)
	}
	families := profile.UnsupportedFamilies()
	if len(families) != 3 || families[0] != "JSON" || families[1] != "Map" || families[2] != "Object" {
		t.Fatalf("unsupported families=%v", families)
	}
	report := EvaluateTypesAgainstEnvelope([]string{
		"Map(String,String)",
		"Object('json')",
		"JSON",
	}, profile.TypeEnvelope())
	if report.Compatible {
		t.Fatalf("report=%#v", report)
	}
	for _, finding := range report.Findings {
		if finding.State != TypeCompatibilityUnknown {
			t.Fatalf("finding=%#v", finding)
		}
	}
}

func TestCompatibilityEvidenceProfileDoesNotGeneralizePatchVersions(t *testing.T) {
	for _, pair := range [][2]string{
		{"23.12.2.1", "20.3.5.21"},
		{"23.12.1.9", "20.3.6.1"},
		{"23.12.2.1", "20.3.6.1"},
	} {
		if profile, ok, err := LoadCompatibilityEvidenceProfile(pair[0], pair[1], StrategyNativeStream); err != nil {
			t.Fatal(err)
		} else if ok {
			t.Fatalf("nearby patch pair unexpectedly inherited evidence: %#v", profile)
		}
	}
}

func TestCompatibilityEvidenceProfileDoesNotGeneralizeStrategy(t *testing.T) {
	if profile, ok, err := LoadCompatibilityEvidenceProfile("23.12.1.9", "20.3.5.21", StrategyBuiltinBackupRestore); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatalf("unproven strategy unexpectedly inherited Native evidence: %#v", profile)
	}
}

func TestTypeEnvelopeDoesNotInferCompatibilityFromWhitespace(t *testing.T) {
	envelope := TypeEnvelope{EvidenceID: "lab", Direction: "23.12.1.9>20.3.5.21", Types: []string{"Nullable(String)"}}
	report := EvaluateTypesAgainstEnvelope([]string{" Nullable( String ) "}, envelope)
	if !report.Compatible {
		t.Fatalf("report=%#v", report)
	}
}

func TestTypeEnvelopeKeepsQuotedEnumLabelsStable(t *testing.T) {
	envelope := TypeEnvelope{EvidenceID: "lab", Direction: "23.12.1.9>20.3.5.21", Types: []string{"Enum8('ok value'=1,'bad'=2)"}}
	report := EvaluateTypesAgainstEnvelope([]string{"Enum8( 'ok value' = 1, 'bad' = 2 )"}, envelope)
	if !report.Compatible {
		t.Fatalf("report=%#v", report)
	}
}
