package sysctlconfig

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strings"
)

type State string

const (
	StateResolved  State = "resolved"
	StateMissing   State = "missing"
	StateAmbiguous State = "ambiguous"
)

type Resolution struct {
	State       State
	Value       string
	SourceClass string
	Digest      string
	Reason      string
}

type Snapshot struct {
	files  []sourceFile
	issues []string
}

type sourceFile struct {
	root     string
	base     string
	contents string
	legacy   bool
}

type viewResult struct {
	state  State
	value  string
	digest string
	reason string
}

var rootPriority = map[string]int{
	"etc": 0, "run": 1, "usr-local": 2, "usr": 3, "lib": 4,
}

func (snapshot Snapshot) Resolve(key string) Resolution {
	if len(snapshot.issues) > 0 {
		return ambiguous(key, "one or more persistent sysctl sources could not be safely evaluated")
	}
	systemd := snapshot.resolveView(key, false)
	procps := snapshot.resolveView(key, true)
	if systemd.state == StateAmbiguous || procps.state == StateAmbiguous {
		return ambiguous(key, "a persistent sysctl assignment has unsupported or ambiguous semantics")
	}
	if systemd.state != procps.state || systemd.value != procps.value {
		return ambiguous(key, "systemd-sysctl and procps --system loading views do not agree")
	}
	if systemd.state == StateMissing {
		return Resolution{
			State: StateMissing, SourceClass: "no_consistent_assignment",
			Digest: digest(key, "missing", systemd.digest, procps.digest),
			Reason: "neither supported loading view resolves a persistent assignment",
		}
	}
	return Resolution{
		State: StateResolved, Value: systemd.value, SourceClass: "systemd_procps_consistent",
		Digest: digest(key, "resolved", systemd.value, systemd.digest, procps.digest),
	}
}

func (snapshot Snapshot) resolveView(key string, procps bool) viewResult {
	chosen := make(map[string]sourceFile)
	var legacy *sourceFile
	for _, file := range snapshot.files {
		if file.legacy {
			if procps {
				copy := file
				legacy = &copy
			}
			continue
		}
		if !procps && file.root == "lib" {
			continue
		}
		current, exists := chosen[file.base]
		if !exists || rootPriority[file.root] < rootPriority[current.root] {
			chosen[file.base] = file
		}
	}
	basenames := make([]string, 0, len(chosen))
	for basename := range chosen {
		basenames = append(basenames, basename)
	}
	sort.Strings(basenames)

	state := StateMissing
	value := ""
	trace := make([]string, 0, len(basenames)+1)
	for _, basename := range basenames {
		file := chosen[basename]
		assignment, assigned, invalid := parseTarget(file.contents, key)
		if invalid {
			return viewResult{state: StateAmbiguous, reason: "unsupported target assignment"}
		}
		if assigned {
			state, value = StateResolved, assignment
			trace = append(trace, file.root+":"+basename+"="+assignment)
		}
	}
	if legacy != nil {
		assignment, assigned, invalid := parseTarget(legacy.contents, key)
		if invalid {
			return viewResult{state: StateAmbiguous, reason: "unsupported legacy target assignment"}
		}
		if assigned {
			state, value = StateResolved, assignment
			trace = append(trace, "legacy="+assignment)
		}
	}
	return viewResult{state: state, value: value, digest: digest(key, strings.Join(trace, "|"))}
}

func parseTarget(contents, target string) (string, bool, bool) {
	value := ""
	found := false
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		line = strings.TrimSpace(stripInlineComment(line))
		if line == "" {
			continue
		}
		ignored := strings.HasPrefix(line, "-")
		if ignored {
			line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		}
		key, rawValue, ok := splitAssignment(line)
		if !ok {
			if strings.Contains(line, target) {
				return "", false, true
			}
			continue
		}
		normalized := strings.ReplaceAll(key, "/", ".")
		if containsGlob(key) {
			matched, _ := path.Match(normalized, target)
			if matched {
				return "", false, true
			}
			continue
		}
		if normalized != target {
			continue
		}
		if ignored || strings.Contains(key, "/") || (rawValue != "0" && rawValue != "1") {
			return "", false, true
		}
		value, found = rawValue, true
	}
	if scanner.Err() != nil {
		return "", false, true
	}
	return value, found, false
}

func splitAssignment(line string) (string, string, bool) {
	if before, after, ok := strings.Cut(line, "="); ok {
		key, value := strings.TrimSpace(before), strings.TrimSpace(after)
		return key, value, key != "" && value != "" && len(strings.Fields(value)) == 1
	}
	fields := strings.Fields(line)
	if len(fields) != 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}

func stripInlineComment(line string) string {
	for index, char := range line {
		if (char == '#' || char == ';') && index > 0 {
			return line[:index]
		}
	}
	return line
}

func containsGlob(value string) bool {
	return strings.ContainsAny(value, "*?[")
}

func ambiguous(key, reason string) Resolution {
	return Resolution{
		State: StateAmbiguous, SourceClass: "ambiguous_sources",
		Digest: digest(key, "ambiguous", reason), Reason: reason,
	}
}

func digest(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return fmt.Sprintf("%x", sum)
}
