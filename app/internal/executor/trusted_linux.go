//go:build linux

package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"setpoint/internal/trustedexec"
)

const maximumSymlinkHops = 64

type validatedExecutableRoot struct {
	configured string
	real       string
}

func resolveSecureExecutable(name string, builtInRoots []string, approvedRoots []trustedexec.Root) (string, error) {
	return resolveSecureExecutableOwnedBy(name, builtInRoots, approvedRoots, 0)
}

func resolveSecureExecutableOwnedBy(
	name string,
	builtInRoots []string,
	approvedRoots []trustedexec.Root,
	trustedOwnerUID uint32,
) (string, error) {
	roots, err := validatedExecutableRoots(builtInRoots, approvedRoots, trustedOwnerUID)
	if err != nil {
		return "", err
	}
	candidates := candidatePaths(name, roots)
	resolved := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		finalPath, found, err := validateExecutableCandidate(candidate, roots, trustedOwnerUID)
		if err != nil {
			return "", errors.Join(ErrExecutableUntrusted, err)
		}
		if found {
			resolved[finalPath] = struct{}{}
		}
	}
	if len(resolved) == 0 {
		return "", fmt.Errorf("%w: %q", ErrCommandNotFound, name)
	}
	if len(resolved) > 1 {
		return "", fmt.Errorf("%w: %q matched %d executables", ErrCommandAmbiguous, name, len(resolved))
	}
	for result := range resolved {
		return result, nil
	}
	panic("unreachable trusted executable resolution")
}

func validatedExecutableRoots(
	builtIn []string,
	approved []trustedexec.Root,
	trustedOwnerUID uint32,
) ([]validatedExecutableRoot, error) {
	roots := make([]validatedExecutableRoot, 0, len(builtIn)+len(approved))
	seen := make(map[string]struct{}, len(builtIn)+len(approved))
	for _, configured := range builtIn {
		root, found, err := validateExecutableRoot(configured, true, trustedOwnerUID)
		if err != nil {
			return nil, errors.Join(ErrTrustedRootInvalid, err)
		}
		if !found {
			continue
		}
		key := root.configured + "\x00" + root.real
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	for _, current := range approved {
		if err := trustedexec.ValidateConfiguredPath(current.Path); err != nil {
			return nil, errors.Join(ErrTrustedRootInvalid, err)
		}
		root, found, err := validateExecutableRoot(current.Path, false, trustedOwnerUID)
		if err != nil {
			return nil, errors.Join(ErrTrustedRootInvalid, err)
		}
		if !found {
			return nil, errors.Join(ErrTrustedRootInvalid, fmt.Errorf("approved root %q does not exist", current.Path))
		}
		key := root.configured + "\x00" + root.real
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, root)
	}
	return roots, nil
}

func validateExecutableRoot(
	configured string,
	allowRootSymlink bool,
	trustedOwnerUID uint32,
) (validatedExecutableRoot, bool, error) {
	if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured {
		return validatedExecutableRoot{}, false, fmt.Errorf("trusted root %q is not a clean absolute path", configured)
	}
	realPath, err := filepath.EvalSymlinks(configured)
	if errors.Is(err, os.ErrNotExist) {
		return validatedExecutableRoot{}, false, nil
	}
	if err != nil {
		return validatedExecutableRoot{}, false, fmt.Errorf("resolve trusted root %q: %w", configured, err)
	}
	realPath = filepath.Clean(realPath)
	if !allowRootSymlink && realPath != configured {
		return validatedExecutableRoot{}, false, fmt.Errorf("approved root %q contains a symlink and must use its canonical path %q", configured, realPath)
	}
	if err := validateSecureDirectoryChain(realPath, trustedOwnerUID); err != nil {
		return validatedExecutableRoot{}, false, fmt.Errorf("validate trusted root %q: %w", configured, err)
	}
	return validatedExecutableRoot{configured: configured, real: realPath}, true, nil
}

func validateSecureDirectoryChain(directory string, trustedOwnerUID uint32) error {
	current := string(filepath.Separator)
	if err := validateSecureDirectory(current, trustedOwnerUID); err != nil {
		return err
	}
	components := strings.Split(strings.TrimPrefix(directory, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect directory %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("canonical trusted root chain still contains symlink %q", current)
		}
		if err := validateSecureDirectoryInfo(current, info, trustedOwnerUID); err != nil {
			return err
		}
	}
	return nil
}

func validateSecureDirectory(path string, trustedOwnerUID uint32) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect directory %q: %w", path, err)
	}
	return validateSecureDirectoryInfo(path, info, trustedOwnerUID)
}

func validateSecureDirectoryInfo(path string, info os.FileInfo, trustedOwnerUID uint32) error {
	if !info.IsDir() {
		return fmt.Errorf("trusted root component %q is not a directory", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("trusted root component %q is group/world writable", path)
	}
	if err := validateTrustedOwner(path, info, trustedOwnerUID); err != nil {
		return err
	}
	return nil
}

func validateTrustedOwner(path string, info os.FileInfo, trustedOwnerUID uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner of %q could not be verified", path)
	}
	if stat.Uid != 0 && stat.Uid != trustedOwnerUID {
		return fmt.Errorf("%q is owned by untrusted uid %d", path, stat.Uid)
	}
	return nil
}

func candidatePaths(name string, roots []validatedExecutableRoot) []string {
	if !filepath.IsAbs(name) {
		result := make([]string, 0, len(roots))
		for _, root := range roots {
			result = append(result, filepath.Join(root.real, name))
		}
		return result
	}
	result := make([]string, 0, 1)
	seen := make(map[string]struct{})
	for _, root := range roots {
		relative, err := filepath.Rel(root.configured, name)
		if err != nil || relativeEscapes(relative) {
			continue
		}
		candidate := filepath.Join(root.real, relative)
		if _, exists := seen[candidate]; !exists {
			seen[candidate] = struct{}{}
			result = append(result, candidate)
		}
	}
	return result
}

func validateExecutableCandidate(
	candidate string,
	roots []validatedExecutableRoot,
	trustedOwnerUID uint32,
) (string, bool, error) {
	info, err := os.Lstat(candidate)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect executable candidate %q: %w", candidate, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if err := validateTrustedOwner(candidate, info, trustedOwnerUID); err != nil {
			return "", false, err
		}
	}
	realPath, err := resolveCandidateSymlinks(candidate, roots, trustedOwnerUID)
	if err != nil {
		return "", false, err
	}
	if !withinAnyRoot(realPath, roots) {
		return "", false, fmt.Errorf("executable %q resolves outside every trusted root", candidate)
	}
	finalInfo, err := os.Stat(realPath)
	if err != nil {
		return "", false, fmt.Errorf("inspect resolved executable %q: %w", realPath, err)
	}
	if !finalInfo.Mode().IsRegular() {
		return "", false, fmt.Errorf("resolved executable %q is not a regular file", realPath)
	}
	if finalInfo.Mode().Perm()&0o111 == 0 {
		return "", false, fmt.Errorf("resolved executable %q has no execute bit", realPath)
	}
	if finalInfo.Mode().Perm()&0o022 != 0 {
		return "", false, fmt.Errorf("resolved executable %q is group/world writable", realPath)
	}
	if err := validateTrustedOwner(realPath, finalInfo, trustedOwnerUID); err != nil {
		return "", false, err
	}
	return realPath, true, nil
}

func resolveCandidateSymlinks(
	candidate string,
	roots []validatedExecutableRoot,
	trustedOwnerUID uint32,
) (string, error) {
	current := string(filepath.Separator)
	pending := pathComponents(candidate)
	symlinkHops := 0
	for len(pending) > 0 {
		next := filepath.Join(current, pending[0])
		pending = pending[1:]
		info, err := os.Lstat(next)
		if err != nil {
			return "", fmt.Errorf("resolve executable path component %q: %w", next, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		symlinkHops++
		if symlinkHops > maximumSymlinkHops {
			return "", fmt.Errorf("executable symlink %q exceeds %d hops", candidate, maximumSymlinkHops)
		}
		if err := validateTrustedOwner(next, info, trustedOwnerUID); err != nil {
			return "", err
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", fmt.Errorf("read executable symlink %q: %w", next, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(next), target)
		}
		target = filepath.Clean(target)
		if !withinAnyRoot(target, roots) {
			return "", fmt.Errorf("executable symlink %q escapes every trusted root to %q", candidate, target)
		}
		pending = append(pathComponents(target), pending...)
		current = string(filepath.Separator)
	}
	return current, nil
}

func pathComponents(value string) []string {
	return strings.Split(strings.TrimPrefix(filepath.Clean(value), string(filepath.Separator)), string(filepath.Separator))
}

func withinAnyRoot(candidate string, roots []validatedExecutableRoot) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root.real, candidate)
		if err == nil && !relativeEscapes(relative) {
			return true
		}
	}
	return false
}

func relativeEscapes(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}
