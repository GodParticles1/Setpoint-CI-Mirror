package executor

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"setpoint/internal/trustedexec"
)

func (executor *OSExecutor) WithTrustedExecutableRoots(roots []trustedexec.Root) (CommandExecutor, error) {
	canonical, err := trustedexec.CanonicalRoots(roots)
	if err != nil {
		return nil, errors.Join(ErrTrustedRootInvalid, err)
	}
	copy := *executor
	copy.approvedRoots = canonical
	return &copy, nil
}

func WithTrustedExecutableRoots(base CommandExecutor, roots []trustedexec.Root) (CommandExecutor, error) {
	if len(roots) == 0 {
		return base, nil
	}
	configurable, ok := base.(interface {
		WithTrustedExecutableRoots([]trustedexec.Root) (CommandExecutor, error)
	})
	if !ok {
		return nil, errors.New("command executor does not support frozen trusted executable roots")
	}
	return configurable.WithTrustedExecutableRoots(roots)
}

func (executor *OSExecutor) resolveExecutable(name string) (string, error) {
	if filepath.IsAbs(name) {
		if filepath.Clean(name) != name {
			return "", errors.New("absolute command path must already be clean")
		}
		return resolveSecureExecutable(name, executor.trustedDirectories, executor.approvedRoots)
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("relative command paths are not allowed")
	}
	return resolveSecureExecutable(name, executor.trustedDirectories, executor.approvedRoots)
}

func defaultTrustedCommandDirectories() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	return []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}
}
