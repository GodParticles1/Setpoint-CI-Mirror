package clickhouse

import "testing"

func TestPreferRuntimeEvidenceDoesNotDowngradeProbe(t *testing.T) {
	profile := NewCapabilityProfile("24.8.1.1")
	profile.Set(CapabilityBuiltinBackupRestore, CapabilitySupported, "runtime_probe", "parser probe succeeded")
	updated := PreferRuntimeEvidence(profile, map[CapabilityID]CapabilityEvidence{
		CapabilityBuiltinBackupRestore: {State: CapabilityUnsupported, Source: "version_hint", Detail: "legacy hint"},
	})
	if !updated.Supported(CapabilityBuiltinBackupRestore) { t.Fatalf("evidence=%#v", updated.Capabilities[CapabilityBuiltinBackupRestore]) }
	if updated.Capabilities[CapabilityBuiltinBackupRestore].Source != "runtime_probe" { t.Fatalf("source=%q", updated.Capabilities[CapabilityBuiltinBackupRestore].Source) }
}

func TestCapabilityProfileAcceptsFutureCapabilityWithoutCoreSchemaChange(t *testing.T) {
	future := CapabilityID("future_zero_copy_transfer")
	profile := NewCapabilityProfile("26.8.1.1")
	profile.Set(future, CapabilitySupported, "runtime_probe", "future probe")
	if !profile.Supported(future) { t.Fatal("future capability was not retained") }
}
