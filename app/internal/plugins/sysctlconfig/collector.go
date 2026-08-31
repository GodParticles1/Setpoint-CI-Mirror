package sysctlconfig

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"setpoint/internal/executor"
)

const (
	maximumCandidates = 128
	maximumContents   = 512 << 10
)

type sourceRoot struct {
	class string
	path  string
}

var sourceRoots = []sourceRoot{
	{class: "etc", path: "/etc/sysctl.d"},
	{class: "run", path: "/run/sysctl.d"},
	{class: "usr-local", path: "/usr/local/lib/sysctl.d"},
	{class: "usr", path: "/usr/lib/sysctl.d"},
	{class: "lib", path: "/lib/sysctl.d"},
}

const legacyBridgeTarget = "/etc/sysctl.conf"

func Collect(ctx context.Context, commandExecutor executor.CommandExecutor) (Snapshot, error) {
	snapshot := Snapshot{}
	candidateCount := 0
	for _, root := range sourceRoots {
		exists, issue, err := safeObject(ctx, commandExecutor, root.path, "-d")
		if err != nil {
			return Snapshot{}, err
		}
		if issue != "" {
			snapshot.issues = append(snapshot.issues, issue)
			continue
		}
		if !exists {
			continue
		}
		result, runErr := commandExecutor.Execute(ctx, executor.Command{
			Name: "find", Args: []string{root.path, "-mindepth", "1", "-maxdepth", "1", "-name", "*.conf", "-printf", "%y|%p\\n"},
		})
		if err := commandFailure("enumerate persistent sysctl sources", result, runErr); err != nil {
			if isTechnical(runErr) || result.StdoutTruncated || result.StderrTruncated {
				return Snapshot{}, err
			}
			snapshot.issues = append(snapshot.issues, "a persistent sysctl source directory could not be enumerated")
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			candidateCount++
			if candidateCount > maximumCandidates {
				snapshot.issues = append(snapshot.issues, "persistent sysctl source count exceeds the evaluation limit")
				break
			}
			kind, candidate, ok := strings.Cut(strings.TrimSpace(line), "|")
			if !ok || path.Dir(candidate) != root.path || path.Ext(candidate) != ".conf" {
				snapshot.issues = append(snapshot.issues, "a persistent sysctl source has an unsupported file type or path")
				continue
			}
			var file sourceFile
			var issue string
			var err error
			switch kind {
			case "f":
				file, issue, err = readSource(ctx, commandExecutor, candidate)
			case "l":
				file, issue, err = readLegacyBridgeSource(ctx, commandExecutor, candidate)
			default:
				issue = "a persistent sysctl source has an unsupported file type or path"
			}
			if err != nil {
				return Snapshot{}, err
			}
			if issue != "" {
				snapshot.issues = append(snapshot.issues, issue)
				continue
			}
			file.root, file.base = root.class, path.Base(candidate)
			snapshot.files = append(snapshot.files, file)
		}
	}

	exists, issue, err := safeObject(ctx, commandExecutor, "/etc/sysctl.conf", "-f")
	if err != nil {
		return Snapshot{}, err
	}
	if issue != "" {
		snapshot.issues = append(snapshot.issues, issue)
	} else if exists {
		file, fileIssue, readErr := readSource(ctx, commandExecutor, "/etc/sysctl.conf")
		if readErr != nil {
			return Snapshot{}, readErr
		}
		if fileIssue != "" {
			snapshot.issues = append(snapshot.issues, fileIssue)
		} else {
			file.root, file.base, file.legacy = "etc", "sysctl.conf", true
			snapshot.files = append(snapshot.files, file)
		}
	}
	if contentsSize(snapshot.files) > maximumContents {
		snapshot.issues = append(snapshot.issues, "persistent sysctl source contents exceed the evaluation limit")
	}
	return snapshot, nil
}

func ReadRuntimeBoolean(ctx context.Context, commandExecutor executor.CommandExecutor, key string) (string, error) {
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-n", key}})
	if err != nil {
		return "", err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", errors.New("sysctl output exceeded the configured limit")
	}
	value := strings.TrimSpace(result.Stdout)
	if value != "0" && value != "1" {
		return "", fmt.Errorf("unexpected boolean sysctl value %q", value)
	}
	return value, nil
}

func safeObject(ctx context.Context, commandExecutor executor.CommandExecutor, target, kind string) (bool, string, error) {
	symlink, err := probe(ctx, commandExecutor, "-L", target)
	if err != nil {
		return false, "", err
	}
	if symlink {
		return false, "a persistent sysctl source path is a symbolic link", nil
	}
	exists, err := probe(ctx, commandExecutor, kind, target)
	if err != nil || !exists {
		return exists, "", err
	}
	mode, owner, group, issue, err := inspectSourceMetadata(ctx, commandExecutor, target)
	if err != nil || issue != "" {
		return false, issue, err
	}
	if owner != "root" || group != "root" || mode&0o022 != 0 {
		return false, "a persistent sysctl source is not root-owned or is writable by group/other", nil
	}
	return true, "", nil
}

func inspectSourceMetadata(ctx context.Context, commandExecutor executor.CommandExecutor, target string) (uint64, string, string, string, error) {
	result, runErr := commandExecutor.Execute(ctx, executor.Command{Name: "stat", Args: []string{"-c", "%a|%U|%G", "--", target}})
	if err := commandFailure("inspect persistent sysctl source", result, runErr); err != nil {
		if isTechnical(runErr) || result.StdoutTruncated || result.StderrTruncated {
			return 0, "", "", "", err
		}
		return 0, "", "", "a persistent sysctl source could not be inspected", nil
	}
	parts := strings.Split(strings.TrimSpace(result.Stdout), "|")
	if len(parts) != 3 {
		return 0, "", "", "a persistent sysctl source has unsupported ownership metadata", nil
	}
	mode, modeErr := strconv.ParseUint(parts[0], 8, 32)
	if modeErr != nil {
		return 0, "", "", "a persistent sysctl source has unsupported ownership metadata", nil
	}
	return mode, parts[1], parts[2], "", nil
}

func readLegacyBridgeSource(ctx context.Context, commandExecutor executor.CommandExecutor, candidate string) (sourceFile, string, error) {
	_, owner, group, issue, err := inspectSourceMetadata(ctx, commandExecutor, candidate)
	if err != nil || issue != "" {
		return sourceFile{}, issue, err
	}
	if owner != "root" || group != "root" {
		return sourceFile{}, "a persistent sysctl source symlink is not root-owned", nil
	}
	result, runErr := commandExecutor.Execute(ctx, executor.Command{
		Name: "readlink", Args: []string{"--", candidate}, OutputLimit: 4096,
	})
	if err := commandFailure("read persistent sysctl source symlink", result, runErr); err != nil {
		if isTechnical(runErr) || result.StdoutTruncated || result.StderrTruncated {
			return sourceFile{}, "", err
		}
		return sourceFile{}, "a persistent sysctl source symlink target could not be read", nil
	}
	linkTarget, ok := singleLinkTarget(result.Stdout)
	if !ok {
		return sourceFile{}, "a persistent sysctl source symlink target is malformed", nil
	}
	resolved := path.Clean(linkTarget)
	if !path.IsAbs(linkTarget) {
		resolved = path.Clean(path.Join(path.Dir(candidate), linkTarget))
	}
	if resolved != legacyBridgeTarget {
		return sourceFile{}, "a persistent sysctl source symlink target is outside the supported legacy bridge", nil
	}
	return readRequiredSource(ctx, commandExecutor, legacyBridgeTarget)
}

func singleLinkTarget(output string) (string, bool) {
	value := strings.TrimSuffix(output, "\n")
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	return value, true
}

func readSource(ctx context.Context, commandExecutor executor.CommandExecutor, target string) (sourceFile, string, error) {
	exists, issue, err := safeObject(ctx, commandExecutor, target, "-f")
	if err != nil || issue != "" || !exists {
		return sourceFile{}, issue, err
	}
	return readSourceContents(ctx, commandExecutor, target)
}

func readRequiredSource(ctx context.Context, commandExecutor executor.CommandExecutor, target string) (sourceFile, string, error) {
	exists, issue, err := safeObject(ctx, commandExecutor, target, "-f")
	if err != nil || issue != "" {
		return sourceFile{}, issue, err
	}
	if !exists {
		return sourceFile{}, "a persistent sysctl source symlink target is not a regular file", nil
	}
	return readSourceContents(ctx, commandExecutor, target)
}

func readSourceContents(ctx context.Context, commandExecutor executor.CommandExecutor, target string) (sourceFile, string, error) {
	result, runErr := commandExecutor.Execute(ctx, executor.Command{Name: "cat", Args: []string{"--", target}})
	if err := commandFailure("read persistent sysctl source", result, runErr); err != nil {
		if isTechnical(runErr) || result.StdoutTruncated || result.StderrTruncated {
			return sourceFile{}, "", err
		}
		return sourceFile{}, "a persistent sysctl source could not be read", nil
	}
	return sourceFile{contents: result.Stdout}, "", nil
}

func probe(ctx context.Context, commandExecutor executor.CommandExecutor, kind, target string) (bool, error) {
	if kind != "-d" && kind != "-f" && kind != "-L" {
		return false, errors.New("persistent sysctl probe predicate is unsupported")
	}
	if !validProbeTarget(target) {
		return false, errors.New("persistent sysctl probe target is outside fixed source roots")
	}
	result, err := commandExecutor.Execute(ctx, executor.Command{Name: "test", Args: []string{kind, target}})
	if err == nil {
		if result.StdoutTruncated || result.StderrTruncated {
			return false, errors.New("test command output exceeded the configured limit")
		}
		return true, nil
	}
	var executionError *executor.Error
	if errors.As(err, &executionError) && executionError.Kind == executor.ErrorExit && executionError.Result.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect persistent sysctl path: %w", err)
}

func validProbeTarget(target string) bool {
	if !path.IsAbs(target) || path.Clean(target) != target {
		return false
	}
	if target == "/etc/sysctl.conf" {
		return true
	}
	for _, root := range sourceRoots {
		if target == root.path || path.Dir(target) == root.path && path.Ext(target) == ".conf" {
			return true
		}
	}
	return false
}

func commandFailure(action string, result executor.Result, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return fmt.Errorf("%s: command output exceeded the configured limit", action)
	}
	return nil
}

func isTechnical(err error) bool {
	if err == nil {
		return false
	}
	var executionError *executor.Error
	if !errors.As(err, &executionError) {
		return true
	}
	return executionError.Kind != executor.ErrorExit
}

func contentsSize(files []sourceFile) int {
	total := 0
	for _, file := range files {
		total += len(file.contents)
	}
	return total
}
