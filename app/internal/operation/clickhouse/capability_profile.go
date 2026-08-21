package clickhouse

import (
	"sort"
	"strings"
)

type CapabilityID string

type CapabilityState string

const (
	CapabilityNativeFormat          CapabilityID = "native_format"
	CapabilityReplacePartitionFrom CapabilityID = "replace_partition_from"
	CapabilityExchangeTables       CapabilityID = "exchange_tables"
	CapabilityBuiltinBackupRestore CapabilityID = "builtin_backup_restore"
	CapabilityRemoteTableFunction  CapabilityID = "remote_table_function"

	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnknown     CapabilityState = "unknown"
)

type CapabilityEvidence struct {
	State  CapabilityState `json:"state"`
	Source string          `json:"source"`
	Detail string          `json:"detail,omitempty"`
}

type CapabilityProfile struct {
	Version      ServerVersion                        `json:"version"`
	Capabilities map[CapabilityID]CapabilityEvidence `json:"capabilities"`
}

func NewCapabilityProfile(version string) CapabilityProfile {
	parsed, err := ParseServerVersion(version)
	if err != nil {
		parsed = ServerVersion{Raw: strings.TrimSpace(version)}
	}
	profile := CapabilityProfile{Version: parsed, Capabilities: map[CapabilityID]CapabilityEvidence{}}
	for _, id := range []CapabilityID{
		CapabilityNativeFormat,
		CapabilityReplacePartitionFrom,
		CapabilityExchangeTables,
		CapabilityBuiltinBackupRestore,
		CapabilityRemoteTableFunction,
	} {
		profile.Capabilities[id] = CapabilityEvidence{State: CapabilityUnknown, Source: "unprobed"}
	}
	return profile
}

func (profile *CapabilityProfile) Set(id CapabilityID, state CapabilityState, source, detail string) {
	if profile.Capabilities == nil {
		profile.Capabilities = map[CapabilityID]CapabilityEvidence{}
	}
	profile.Capabilities[id] = CapabilityEvidence{State: state, Source: strings.TrimSpace(source), Detail: strings.TrimSpace(detail)}
}

func (profile CapabilityProfile) State(id CapabilityID) CapabilityState {
	if evidence, ok := profile.Capabilities[id]; ok {
		return evidence.State
	}
	return CapabilityUnknown
}

func (profile CapabilityProfile) Supported(id CapabilityID) bool {
	return profile.State(id) == CapabilitySupported
}

func (profile CapabilityProfile) KnownIDs() []CapabilityID {
	ids := make([]CapabilityID, 0, len(profile.Capabilities))
	for id := range profile.Capabilities {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

type CapabilityIntersection struct {
	Source CapabilityProfile `json:"source"`
	Target CapabilityProfile `json:"target"`
}

func (intersection CapabilityIntersection) BothSupport(id CapabilityID) bool {
	return intersection.Source.Supported(id) && intersection.Target.Supported(id)
}

// PreferRuntimeEvidence merges capability evidence without allowing a weaker
// version hint to overwrite an explicit runtime probe. This makes feature
// detection forward-compatible: new ClickHouse releases can be recognized by
// probes first, while version metadata remains advisory.
func PreferRuntimeEvidence(profile CapabilityProfile, hints map[CapabilityID]CapabilityEvidence) CapabilityProfile {
	for id, hint := range hints {
		current, exists := profile.Capabilities[id]
		if exists && current.Source == "runtime_probe" {
			continue
		}
		profile.Set(id, hint.State, hint.Source, hint.Detail)
	}
	return profile
}
