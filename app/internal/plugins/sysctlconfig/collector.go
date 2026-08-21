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

func Collect(ctx context.Context, commandExecutor executor.CommandExecutor) (Snapshot, error) {
	snapshot := Snapshot{}
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
			if len(snapshot.files) >= maximumCandidates {
				snapshot.issues = append(snapshot.issues, "persistent sysctl source count exceeds the evaluation limit")
				break
			}
			kind, candidate, ok := strings.Cut(strings.TrimSpace(line), "|")
			if !ok || kind != "f" || path.Dir(candidate) != root.path || path.Ext(candidate) != ".conf" {
				snapshot.issues = append(snapshot.issues, "a persistent sysctl source has an unsupported file type or path")
				continue
			}
			file, issue, err := readSource(ctx, commandExecutor, candidate)
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
	result, runErr := commandExecutor.Execute(ctx, executor.Command{Name: "stat", Args: []string{"-c", "%a|%U|%G", "--", target}})
	if err := commandFailure("inspect persistent sysctl source", result, runErr); err != nil {
		if isTechnical(runErr) || result.StdoutTruncated || result.StderrTruncated {
			return false, "", err
		}
		return false, "a persistent sysctl source could not be inspected", nil
	}
	parts := strings.Split(strings.TrimSpace(result.Stdout), "|")
	if len(parts) != 3 {
		return false, "a persistent sysctl source has unsupported ownership metadata", nil
	}
	mode, modeErr := strconv.ParseUint(parts[0], 8, 32)
	if modeErr != nil || parts[1] != "root" || parts[2] != "root" || mode&0o022 != 0 {
		return false, "a persistent sysctl source is not root-owned or is writable by group/other", nil
	}
	return true, "", nil
}

func readSource(ctx context.Context, commandExecutor executor.CommandExecutor, target string) (sourceFile, string, error) {
	exists, issue, err := safeObject(ctx, commandExecutor, target, "-f")
	if err != nil || issue != "" || !exists {
		return sourceFile{}, issue, err
	}
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
