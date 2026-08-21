package clickhouse

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

const compatibilityEvidenceSchemaV1 = "clickhouse.compatibility_evidence.v1"

// compatibilityProfiles contains only reviewed, compile-time evidence owned by
// the ClickHouse Operation capability. It is not a dynamic plugin/config feed.
// New physical evidence is added as data; evaluator semantics remain in Go.
//
//go:embed compatibility_profiles/*.json
var compatibilityProfiles embed.FS

type CompatibilityEvidenceProfile struct {
	SchemaVersion           string     `json:"schema_version"`
	EvidenceID              string     `json:"evidence_id"`
	SourceVersion           string     `json:"source_version"`
	TargetVersion           string     `json:"target_version"`
	Strategy                StrategyID `json:"strategy"`
	VerifiedTypes           []string   `json:"verified_types"`
	UnsupportedTypeFamilies []string   `json:"unsupported_type_families,omitempty"`
}

func LoadCompatibilityEvidenceProfile(sourceVersion, targetVersion string, strategy StrategyID) (CompatibilityEvidenceProfile, bool, error) {
	sourceVersion = strings.TrimSpace(sourceVersion)
	targetVersion = strings.TrimSpace(targetVersion)
	if sourceVersion == "" || targetVersion == "" || strategy == "" {
		return CompatibilityEvidenceProfile{}, false, errors.New("source version, target version and strategy are required")
	}
	profiles, err := loadCompatibilityEvidenceProfiles()
	if err != nil {
		return CompatibilityEvidenceProfile{}, false, err
	}
	requestedKey := compatibilityEvidenceProfileKey(sourceVersion, targetVersion, strategy)
	for _, profile := range profiles {
		if compatibilityEvidenceProfileKey(profile.SourceVersion, profile.TargetVersion, profile.Strategy) == requestedKey {
			return profile, true, nil
		}
	}
	return CompatibilityEvidenceProfile{}, false, nil
}

func loadCompatibilityEvidenceProfiles() ([]CompatibilityEvidenceProfile, error) {
	entries, err := compatibilityProfiles.ReadDir("compatibility_profiles")
	if err != nil {
		return nil, fmt.Errorf("read ClickHouse compatibility profiles: %w", err)
	}
	profiles := make([]CompatibilityEvidenceProfile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := compatibilityProfiles.ReadFile(path.Join("compatibility_profiles", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read ClickHouse compatibility profile %s: %w", entry.Name(), err)
		}
		var profile CompatibilityEvidenceProfile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return nil, fmt.Errorf("decode ClickHouse compatibility profile %s: %w", entry.Name(), err)
		}
		if err := validateCompatibilityEvidenceProfile(profile); err != nil {
			return nil, fmt.Errorf("invalid ClickHouse compatibility profile %s: %w", entry.Name(), err)
		}
		profiles = append(profiles, profile)
	}
	if err := validateCompatibilityEvidenceProfileCatalog(profiles); err != nil {
		return nil, err
	}
	sort.Slice(profiles, func(i, j int) bool {
		return compatibilityEvidenceProfileKey(profiles[i].SourceVersion, profiles[i].TargetVersion, profiles[i].Strategy) <
			compatibilityEvidenceProfileKey(profiles[j].SourceVersion, profiles[j].TargetVersion, profiles[j].Strategy)
	})
	return profiles, nil
}

func validateCompatibilityEvidenceProfileCatalog(profiles []CompatibilityEvidenceProfile) error {
	seen := make(map[string]string, len(profiles))
	for _, profile := range profiles {
		if err := validateCompatibilityEvidenceProfile(profile); err != nil {
			return err
		}
		key := compatibilityEvidenceProfileKey(profile.SourceVersion, profile.TargetVersion, profile.Strategy)
		if previousEvidence, exists := seen[key]; exists {
			return fmt.Errorf("duplicate compatibility evidence profile %q: evidence %q conflicts with %q", key, previousEvidence, profile.EvidenceID)
		}
		seen[key] = profile.EvidenceID
	}
	return nil
}

func compatibilityEvidenceProfileKey(sourceVersion, targetVersion string, strategy StrategyID) string {
	return strings.TrimSpace(sourceVersion) + ">" + strings.TrimSpace(targetVersion) + "|" + strings.TrimSpace(string(strategy))
}

func validateCompatibilityEvidenceProfile(profile CompatibilityEvidenceProfile) error {
	if profile.SchemaVersion != compatibilityEvidenceSchemaV1 {
		return fmt.Errorf("unsupported schema version %q", profile.SchemaVersion)
	}
	if strings.TrimSpace(profile.EvidenceID) == "" || strings.TrimSpace(profile.SourceVersion) == "" || strings.TrimSpace(profile.TargetVersion) == "" {
		return errors.New("evidence id and exact source/target versions are required")
	}
	if !knownCompatibilityStrategy(profile.Strategy) {
		return fmt.Errorf("unknown strategy %q", profile.Strategy)
	}
	if _, err := ParseServerVersion(profile.SourceVersion); err != nil {
		return fmt.Errorf("source version: %w", err)
	}
	if _, err := ParseServerVersion(profile.TargetVersion); err != nil {
		return fmt.Errorf("target version: %w", err)
	}
	if len(profile.VerifiedTypes) == 0 {
		return errors.New("at least one physically verified exact type is required")
	}

	verified := make(map[string]struct{}, len(profile.VerifiedTypes))
	for _, value := range profile.VerifiedTypes {
		normalized := normalizeClickHouseType(value)
		if normalized == "" {
			return errors.New("verified type must not be empty")
		}
		if _, exists := verified[normalized]; exists {
			return fmt.Errorf("duplicate verified type %q", normalized)
		}
		verified[normalized] = struct{}{}
	}

	families := make(map[string]struct{}, len(profile.UnsupportedTypeFamilies))
	for _, value := range profile.UnsupportedTypeFamilies {
		family := strings.TrimSpace(value)
		if family == "" {
			return errors.New("unsupported type family must not be empty")
		}
		if _, exists := families[family]; exists {
			return fmt.Errorf("duplicate unsupported type family %q", family)
		}
		families[family] = struct{}{}
		for exactType := range verified {
			if exactType == family || strings.HasPrefix(exactType, family+"(") {
				return fmt.Errorf("type family %q cannot be both physically verified and unsupported", family)
			}
		}
	}
	return nil
}

func knownCompatibilityStrategy(strategy StrategyID) bool {
	if strategy == "" {
		return false
	}
	for _, descriptor := range StrategyCatalog() {
		if descriptor.ID == strategy {
			return true
		}
	}
	return false
}

func (profile CompatibilityEvidenceProfile) TypeEnvelope() TypeEnvelope {
	return TypeEnvelope{
		EvidenceID: profile.EvidenceID,
		Direction:  profile.SourceVersion + ">" + profile.TargetVersion,
		Types:      append([]string(nil), profile.VerifiedTypes...),
	}
}

func (profile CompatibilityEvidenceProfile) UnsupportedFamilies() []string {
	values := append([]string(nil), profile.UnsupportedTypeFamilies...)
	sort.Strings(values)
	return values
}
