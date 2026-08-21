package trustedexec

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

type Scope string

const (
	ScopeBuiltIn Scope = "built_in"
	ScopeSite    Scope = "site"
	ScopeNode    Scope = "node"
)

const ValidationPendingAgent = "pending_agent_validation"

type Root struct {
	Path   string `json:"path"`
	Scope  Scope  `json:"scope"`
	Source string `json:"source"`
}

type ConfiguredRoot struct {
	Root
	ValidationStatus string `json:"validation_status"`
}

func NormalizeConfiguredPaths(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if err := ValidateConfiguredPath(value); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func ValidateConfiguredPath(value string) error {
	if value == "" {
		return errors.New("trusted executable root must not be empty")
	}
	if len(value) > 4096 || strings.ContainsRune(value, 0) {
		return errors.New("trusted executable root is too long or contains NUL")
	}
	if !path.IsAbs(value) {
		return fmt.Errorf("trusted executable root %q must be an absolute POSIX path", value)
	}
	if path.Clean(value) != value {
		return fmt.Errorf("trusted executable root %q must already be clean and contain no traversal", value)
	}
	if value == "/" {
		return errors.New("filesystem root cannot be approved as a trusted executable root")
	}
	for _, temporary := range []string{"/tmp", "/var/tmp", "/dev/shm", "/run/user"} {
		if value == temporary || strings.HasPrefix(value, temporary+"/") {
			return fmt.Errorf("temporary path %q cannot be a trusted executable root", value)
		}
	}
	return nil
}

func NewConfiguredRoots(scope Scope, source string, paths []string) ([]ConfiguredRoot, error) {
	if scope != ScopeSite && scope != ScopeNode {
		return nil, fmt.Errorf("configured trusted executable root scope %q is not allowed", scope)
	}
	if strings.TrimSpace(source) == "" {
		return nil, errors.New("trusted executable root source is required")
	}
	normalized, err := NormalizeConfiguredPaths(paths)
	if err != nil {
		return nil, err
	}
	result := make([]ConfiguredRoot, 0, len(normalized))
	for _, current := range normalized {
		result = append(result, ConfiguredRoot{
			Root:             Root{Path: current, Scope: scope, Source: source},
			ValidationStatus: ValidationPendingAgent,
		})
	}
	return result, nil
}

func FreezeConfiguredRoots(values []ConfiguredRoot) ([]Root, error) {
	roots := make([]Root, 0, len(values))
	for _, current := range values {
		roots = append(roots, current.Root)
	}
	return CanonicalRoots(roots)
}

func CanonicalRoots(values []Root) ([]Root, error) {
	result := append([]Root(nil), values...)
	for index := range result {
		result[index].Path = strings.TrimSpace(result[index].Path)
		result[index].Source = strings.TrimSpace(result[index].Source)
		if err := ValidateConfiguredPath(result[index].Path); err != nil {
			return nil, err
		}
		if result[index].Scope != ScopeSite && result[index].Scope != ScopeNode {
			return nil, fmt.Errorf("trusted executable root scope %q is not configurable", result[index].Scope)
		}
		if result[index].Source == "" {
			return nil, errors.New("trusted executable root source is required")
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Scope != result[right].Scope {
			return result[left].Scope < result[right].Scope
		}
		return result[left].Source < result[right].Source
	})
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, fmt.Errorf("duplicate trusted executable root %q", result[index].Path)
		}
	}
	return result, nil
}

func Paths(values []ConfiguredRoot, scope Scope) []string {
	result := make([]string, 0, len(values))
	for _, current := range values {
		if current.Scope == scope {
			result = append(result, current.Path)
		}
	}
	sort.Strings(result)
	return result
}
