package clickhouse

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type ServerVersion struct {
	Raw      string `json:"raw"`
	Major    int    `json:"major"`
	Minor    int    `json:"minor"`
	Patch    int    `json:"patch"`
	Revision int    `json:"revision"`
}

func ParseServerVersion(raw string) (ServerVersion, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ServerVersion{}, errors.New("ClickHouse version is empty")
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return !unicode.IsDigit(r) })
	if len(parts) < 2 {
		return ServerVersion{}, fmt.Errorf("unsupported ClickHouse version %q", raw)
	}
	values := make([]int, 4)
	for index := 0; index < len(values) && index < len(parts); index++ {
		value, err := strconv.Atoi(parts[index])
		if err != nil {
			return ServerVersion{}, fmt.Errorf("parse ClickHouse version %q: %w", raw, err)
		}
		values[index] = value
	}
	return ServerVersion{Raw: raw, Major: values[0], Minor: values[1], Patch: values[2], Revision: values[3]}, nil
}

func (version ServerVersion) Compare(other ServerVersion) int {
	left := [...]int{version.Major, version.Minor, version.Patch, version.Revision}
	right := [...]int{other.Major, other.Minor, other.Patch, other.Revision}
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func (version ServerVersion) AtLeast(major, minor int) bool {
	return version.Major > major || (version.Major == major && version.Minor >= minor)
}

func (version ServerVersion) SameMajorMinor(other ServerVersion) bool {
	return version.Major == other.Major && version.Minor == other.Minor
}

type VersionRelation string

const (
	VersionRelationExact          VersionRelation = "exact"
	VersionRelationPatchDifferent VersionRelation = "patch_different"
	VersionRelationMinorDifferent VersionRelation = "minor_different"
	VersionRelationMajorDifferent VersionRelation = "major_different"
	VersionRelationUnknown        VersionRelation = "unknown"
)

func CompareVersionStrings(source, target string) VersionRelation {
	left, leftErr := ParseServerVersion(source)
	right, rightErr := ParseServerVersion(target)
	if leftErr != nil || rightErr != nil {
		return VersionRelationUnknown
	}
	if left.Compare(right) == 0 {
		return VersionRelationExact
	}
	if left.Major != right.Major {
		return VersionRelationMajorDifferent
	}
	if left.Minor != right.Minor {
		return VersionRelationMinorDifferent
	}
	return VersionRelationPatchDifferent
}
