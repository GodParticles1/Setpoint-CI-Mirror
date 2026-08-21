package clickhouse

import "testing"

func TestCompareFingerprintsUsesRowsAndTwoHashes(t *testing.T) {
	source := DataFingerprint{Rows: 10, HashSum64: "100", HashXor64: "7"}
	if !CompareFingerprints(source, source).Passed { t.Fatal("equal fingerprints rejected") }
	target := source; target.HashXor64 = "8"
	if CompareFingerprints(source, target).Passed { t.Fatal("hash mismatch accepted") }
}
