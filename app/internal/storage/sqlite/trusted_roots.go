package sqlite

import (
	"encoding/json"
	"fmt"

	"setpoint/internal/trustedexec"
)

func encodeTrustedRootPaths(values []trustedexec.ConfiguredRoot, scope trustedexec.Scope) (string, error) {
	encoded, err := json.Marshal(trustedexec.Paths(values, scope))
	if err != nil {
		return "", fmt.Errorf("encode trusted executable roots: %w", err)
	}
	return string(encoded), nil
}

func decodeTrustedRoots(raw string, scope trustedexec.Scope, source string) ([]trustedexec.ConfiguredRoot, error) {
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil {
		return nil, fmt.Errorf("decode trusted executable roots: %w", err)
	}
	return trustedexec.NewConfiguredRoots(scope, source, paths)
}

func sameRootPaths(left, right []trustedexec.ConfiguredRoot, scope trustedexec.Scope) bool {
	leftPaths := trustedexec.Paths(left, scope)
	rightPaths := trustedexec.Paths(right, scope)
	if len(leftPaths) != len(rightPaths) {
		return false
	}
	for index := range leftPaths {
		if leftPaths[index] != rightPaths[index] {
			return false
		}
	}
	return true
}
