package clickhouse

import "testing"

func TestParseServerVersionAcceptsOfficialAndVendorSuffixes(t *testing.T) {
	cases := []struct {
		raw         string
		major, minor int
		patch       int
	}{
		{"22.3.15.33", 22, 3, 15},
		{"24.8.7.41.altinitystable", 24, 8, 7},
		{"25.3.1", 25, 3, 1},
	}
	for _, test := range cases {
		version, err := ParseServerVersion(test.raw)
		if err != nil { t.Fatalf("ParseServerVersion(%q): %v", test.raw, err) }
		if version.Major != test.major || version.Minor != test.minor || version.Patch != test.patch {
			t.Fatalf("ParseServerVersion(%q)=%#v", test.raw, version)
		}
	}
}

func TestCompareVersionStringsKeepsCompatibilityRelationExplicit(t *testing.T) {
	if got := CompareVersionStrings("24.8.1.1", "24.8.9.2"); got != VersionRelationPatchDifferent { t.Fatalf("relation=%s", got) }
	if got := CompareVersionStrings("24.8.1.1", "24.9.1.1"); got != VersionRelationMinorDifferent { t.Fatalf("relation=%s", got) }
	if got := CompareVersionStrings("24.8.1.1", "25.1.1.1"); got != VersionRelationMajorDifferent { t.Fatalf("relation=%s", got) }
}
