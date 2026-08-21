package clickhouse

import (
	"sort"
	"strings"
)

type CrossVersionDirection string

type CrossVersionState string

const (
	CrossVersionSame      CrossVersionDirection = "same"
	CrossVersionUpgrade   CrossVersionDirection = "upgrade"
	CrossVersionDowngrade CrossVersionDirection = "downgrade"

	CrossVersionAllowed CrossVersionState = "allowed"
	CrossVersionBlocked CrossVersionState = "blocked"
	CrossVersionUnknown CrossVersionState = "unknown"
)

// CrossVersionRule represents an explicit reviewed rule for one exact source
// and target server version. Physical evidence must never be generalized to a
// nearby patch/minor release merely because the release line looks similar.
type CrossVersionRule struct {
	SourceVersion string                `json:"source_version"`
	TargetVersion string                `json:"target_version"`
	Direction     CrossVersionDirection `json:"direction"`
	State         CrossVersionState     `json:"state"`
	Strategies    []StrategyID          `json:"strategies,omitempty"`
	Evidence      string                `json:"evidence,omitempty"`
	Reason        string                `json:"reason,omitempty"`
}

type CrossVersionDecision struct {
	Relation   VersionRelation       `json:"relation"`
	Direction  CrossVersionDirection `json:"direction"`
	State      CrossVersionState     `json:"state"`
	Strategies []StrategyID          `json:"strategies,omitempty"`
	Evidence   string                `json:"evidence,omitempty"`
	Reason     string                `json:"reason"`
}

type CrossVersionRegistry struct {
	rules map[string]CrossVersionRule
}

func NewCrossVersionRegistry(rules ...CrossVersionRule) *CrossVersionRegistry {
	registry := &CrossVersionRegistry{rules: make(map[string]CrossVersionRule)}
	for _, rule := range rules {
		registry.Add(rule)
	}
	return registry
}

func (registry *CrossVersionRegistry) Add(rule CrossVersionRule) {
	if registry.rules == nil {
		registry.rules = make(map[string]CrossVersionRule)
	}
	rule.SourceVersion = strings.TrimSpace(rule.SourceVersion)
	rule.TargetVersion = strings.TrimSpace(rule.TargetVersion)
	source, sourceErr := ParseServerVersion(rule.SourceVersion)
	target, targetErr := ParseServerVersion(rule.TargetVersion)
	if sourceErr != nil || targetErr != nil {
		return
	}
	if rule.Direction == "" {
		rule.Direction = directionForComponents(source.Major, source.Minor, target.Major, target.Minor)
	}
	rule.Strategies = uniqueStrategies(rule.Strategies)
	registry.rules[crossVersionRuleKey(rule.SourceVersion, rule.TargetVersion)] = rule
}

func (registry *CrossVersionRegistry) Decide(sourceRaw, targetRaw string) CrossVersionDecision {
	sourceRaw = strings.TrimSpace(sourceRaw)
	targetRaw = strings.TrimSpace(targetRaw)
	relation := CompareVersionStrings(sourceRaw, targetRaw)
	source, sourceErr := ParseServerVersion(sourceRaw)
	target, targetErr := ParseServerVersion(targetRaw)
	if sourceErr != nil || targetErr != nil {
		return CrossVersionDecision{Relation: VersionRelationUnknown, State: CrossVersionUnknown, Reason: "source or target ClickHouse version cannot be parsed"}
	}
	direction := directionForComponents(source.Major, source.Minor, target.Major, target.Minor)
	if relation == VersionRelationExact || relation == VersionRelationPatchDifferent {
		return CrossVersionDecision{
			Relation: relation, Direction: direction, State: CrossVersionAllowed,
			Strategies: []StrategyID{StrategyNativeStream}, Evidence: "built_in_same_release_line_policy",
			Reason: "same major/minor release line may be analyzed with exact schema and runtime capability checks; Apply still requires normal safety gates",
		}
	}
	if registry != nil {
		if rule, ok := registry.rules[crossVersionRuleKey(sourceRaw, targetRaw)]; ok {
			return CrossVersionDecision{Relation: relation, Direction: rule.Direction, State: rule.State, Strategies: append([]StrategyID(nil), rule.Strategies...), Evidence: rule.Evidence, Reason: rule.Reason}
		}
	}
	return CrossVersionDecision{
		Relation: relation, Direction: direction, State: CrossVersionUnknown,
		Reason: "cross-major/minor migration is not enabled until an explicit physical compatibility rule exists for this exact source/target version pair",
	}
}

func directionForComponents(sourceMajor, sourceMinor, targetMajor, targetMinor int) CrossVersionDirection {
	if sourceMajor == targetMajor && sourceMinor == targetMinor {
		return CrossVersionSame
	}
	if targetMajor > sourceMajor || (targetMajor == sourceMajor && targetMinor > sourceMinor) {
		return CrossVersionUpgrade
	}
	return CrossVersionDowngrade
}

func crossVersionRuleKey(sourceVersion, targetVersion string) string {
	return strings.TrimSpace(sourceVersion) + ">" + strings.TrimSpace(targetVersion)
}

func uniqueStrategies(values []StrategyID) []StrategyID {
	seen := make(map[StrategyID]struct{})
	result := make([]StrategyID, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(string(value)) == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
