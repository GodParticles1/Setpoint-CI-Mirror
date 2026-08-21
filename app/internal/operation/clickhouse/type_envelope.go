package clickhouse

import (
	"fmt"
	"sort"
	"strings"
)

type TypeCompatibilityState string

const (
	TypeCompatibilityVerified TypeCompatibilityState = "verified"
	TypeCompatibilityUnknown  TypeCompatibilityState = "unknown"
	TypeCompatibilityBlocked  TypeCompatibilityState = "blocked"
)

type TypeEnvelope struct {
	EvidenceID string   `json:"evidence_id"`
	Direction  string   `json:"direction"`
	Types      []string `json:"types"`
}

type TypeCompatibilityFinding struct {
	Type   string                 `json:"type"`
	State  TypeCompatibilityState `json:"state"`
	Reason string                 `json:"reason"`
}

type TypeCompatibilityReport struct {
	Compatible bool                       `json:"compatible"`
	Findings   []TypeCompatibilityFinding `json:"findings"`
}

// EvaluateTypesAgainstEnvelope is intentionally version-agnostic, exact and
// fail-closed. Physical version-pair/type evidence is loaded separately from
// immutable ClickHouse capability profiles. The evaluator never infers support
// from a type family, a nearby release, or apparent wire-format similarity.
func EvaluateTypesAgainstEnvelope(columnTypes []string, envelope TypeEnvelope) TypeCompatibilityReport {
	verified := make(map[string]struct{}, len(envelope.Types))
	for _, value := range envelope.Types {
		value = normalizeClickHouseType(value)
		if value != "" {
			verified[value] = struct{}{}
		}
	}

	report := TypeCompatibilityReport{Compatible: true}
	seen := map[string]struct{}{}
	for _, raw := range columnTypes {
		typeName := normalizeClickHouseType(raw)
		if typeName == "" {
			report.Compatible = false
			report.Findings = append(report.Findings, TypeCompatibilityFinding{Type: raw, State: TypeCompatibilityBlocked, Reason: "empty ClickHouse type expression"})
			continue
		}
		if _, duplicate := seen[typeName]; duplicate {
			continue
		}
		seen[typeName] = struct{}{}
		if _, ok := verified[typeName]; ok {
			report.Findings = append(report.Findings, TypeCompatibilityFinding{Type: typeName, State: TypeCompatibilityVerified, Reason: "exact type expression is inside the physically verified directional envelope"})
			continue
		}
		report.Compatible = false
		report.Findings = append(report.Findings, TypeCompatibilityFinding{Type: typeName, State: TypeCompatibilityUnknown, Reason: fmt.Sprintf("type is not physically verified by %s for direction %s", envelope.EvidenceID, envelope.Direction)})
	}
	sort.Slice(report.Findings, func(i, j int) bool { return report.Findings[i].Type < report.Findings[j].Type })
	return report
}

func normalizeClickHouseType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	inQuote := false
	for _, r := range value {
		switch r {
		case '\'':
			inQuote = !inQuote
			builder.WriteRune(r)
		case ' ', '\t', '\n', '\r':
			if inQuote {
				builder.WriteRune(r)
			}
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
