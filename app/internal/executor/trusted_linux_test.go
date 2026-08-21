//go:build linux

package executor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"setpoint/internal/trustedexec"
)

func TestBuiltInTrustedExecutablePasses(t *testing.T) {
	root := secureResolverTestRoot(t)
	executable := writeResolverExecutable(t, root, "nginx", 0o755)
	resolved, err := resolveTestExecutable("nginx", []string{root}, nil)
	if err != nil || resolved != executable {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestSystemTrustedRegularFilePasses(t *testing.T) {
	root := secureResolverTestRoot(t)
	executable := writeResolverExecutable(t, root, "tool", 0o700)
	resolved, err := resolveTestExecutable(executable, []string{root}, nil)
	if err != nil || resolved != executable {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestAbsoluteExecutableUsesConfiguredAliasOfCanonicalBuiltInRoot(t *testing.T) {
	realRoot := secureResolverTestRoot(t)
	aliasParent := secureResolverTestRoot(t)
	aliasRoot := filepath.Join(aliasParent, "bin")
	symlinkResolverFile(t, realRoot, aliasRoot)
	executable := writeResolverExecutable(t, realRoot, "tool", 0o700)

	resolved, err := resolveTestExecutable(
		filepath.Join(aliasRoot, "tool"), []string{realRoot, aliasRoot}, nil,
	)
	if err != nil || resolved != executable {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestSystemTrustedSymlinkToSystemTrustedTargetPasses(t *testing.T) {
	entryRoot := secureResolverTestRoot(t)
	targetRoot := secureResolverTestRoot(t)
	target := writeResolverExecutable(t, targetRoot, "nginx-real", 0o755)
	symlinkResolverFile(t, target, filepath.Join(entryRoot, "nginx"))
	resolved, err := resolveTestExecutable("nginx", []string{entryRoot, targetRoot}, nil)
	if err != nil || resolved != target {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestSystemTrustedSymlinkToApprovedRootPasses(t *testing.T) {
	entryRoot := secureResolverTestRoot(t)
	approvedRoot := secureResolverTestRoot(t)
	target := writeResolverExecutable(t, approvedRoot, "nginx", 0o755)
	symlinkResolverFile(t, target, filepath.Join(entryRoot, "nginx"))
	resolved, err := resolveTestExecutable("nginx", []string{entryRoot}, []trustedexec.Root{approvedNodeRoot(approvedRoot)})
	if err != nil || resolved != target {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestSystemTrustedSymlinkToUnapprovedPathFails(t *testing.T) {
	entryRoot := secureResolverTestRoot(t)
	unapprovedRoot := secureResolverTestRoot(t)
	target := writeResolverExecutable(t, unapprovedRoot, "nginx", 0o755)
	symlinkResolverFile(t, target, filepath.Join(entryRoot, "nginx"))
	_, err := resolveTestExecutable("nginx", []string{entryRoot}, nil)
	if !errors.Is(err, ErrExecutableUntrusted) {
		t.Fatalf("error=%v, want ErrExecutableUntrusted", err)
	}
}

func TestApprovedRootExecutablePasses(t *testing.T) {
	approvedRoot := secureResolverTestRoot(t)
	target := writeResolverExecutable(t, approvedRoot, "nginx", 0o755)
	resolved, err := resolveTestExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(approvedRoot)})
	if err != nil || resolved != target {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestExecutableSymlinkEscapeFromApprovedRootFails(t *testing.T) {
	approvedRoot := secureResolverTestRoot(t)
	outsideRoot := secureResolverTestRoot(t)
	target := writeResolverExecutable(t, outsideRoot, "nginx", 0o755)
	symlinkResolverFile(t, target, filepath.Join(approvedRoot, "nginx"))
	_, err := resolveTestExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(approvedRoot)})
	if !errors.Is(err, ErrExecutableUntrusted) {
		t.Fatalf("error=%v, want ErrExecutableUntrusted", err)
	}
}

func TestExecutableSymlinkChainCannotLeaveAndReenterApprovedRoot(t *testing.T) {
	approvedRoot := secureResolverTestRoot(t)
	outsideRoot := secureResolverTestRoot(t)
	writeResolverExecutable(t, outsideRoot, "nginx-real", 0o755)
	symlinkResolverFile(t, outsideRoot, filepath.Join(approvedRoot, "outside"))
	symlinkResolverFile(t, filepath.Join(approvedRoot, "outside", "nginx-real"), filepath.Join(approvedRoot, "nginx"))
	_, err := resolveTestExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(approvedRoot)})
	if !errors.Is(err, ErrExecutableUntrusted) {
		t.Fatalf("error=%v, want ErrExecutableUntrusted", err)
	}
}

func TestProductionResolverRequiresRootOwnedApprovedDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test process already owns fixtures as root")
	}
	root := secureResolverTestRoot(t)
	writeResolverExecutable(t, root, "nginx", 0o755)
	_, err := resolveSecureExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(root)})
	if !errors.Is(err, ErrTrustedRootInvalid) {
		t.Fatalf("error=%v, want ErrTrustedRootInvalid", err)
	}
}

func TestGroupWritableTrustedRootFails(t *testing.T) {
	root := secureResolverTestRoot(t)
	writeResolverExecutable(t, root, "nginx", 0o755)
	if err := os.Chmod(root, 0o775); err != nil {
		t.Fatal(err)
	}
	_, err := resolveTestExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(root)})
	if !errors.Is(err, ErrTrustedRootInvalid) {
		t.Fatalf("error=%v, want ErrTrustedRootInvalid", err)
	}
}

func TestWorldWritableTrustedRootFails(t *testing.T) {
	root := secureResolverTestRoot(t)
	writeResolverExecutable(t, root, "nginx", 0o755)
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	_, err := resolveTestExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(root)})
	if !errors.Is(err, ErrTrustedRootInvalid) {
		t.Fatalf("error=%v, want ErrTrustedRootInvalid", err)
	}
}

func TestGroupOrWorldWritableExecutableFails(t *testing.T) {
	for _, mode := range []os.FileMode{0o775, 0o757} {
		t.Run(mode.String(), func(t *testing.T) {
			root := secureResolverTestRoot(t)
			writeResolverExecutable(t, root, "nginx", mode)
			_, err := resolveTestExecutable("nginx", nil, []trustedexec.Root{approvedNodeRoot(root)})
			if !errors.Is(err, ErrExecutableUntrusted) {
				t.Fatalf("mode=%#o error=%v, want ErrExecutableUntrusted", mode, err)
			}
		})
	}
}

func TestDuplicateCandidateResolvesOnce(t *testing.T) {
	root := secureResolverTestRoot(t)
	target := writeResolverExecutable(t, root, "nginx", 0o755)
	resolved, err := resolveTestExecutable("nginx", []string{root, root}, nil)
	if err != nil || resolved != target {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
}

func TestMultipleDistinctTrustedExecutablesFailClosed(t *testing.T) {
	first := secureResolverTestRoot(t)
	second := secureResolverTestRoot(t)
	writeResolverExecutable(t, first, "nginx", 0o755)
	writeResolverExecutable(t, second, "nginx", 0o755)
	_, err := resolveTestExecutable("nginx", []string{first, second}, nil)
	if !errors.Is(err, ErrCommandAmbiguous) {
		t.Fatalf("error=%v, want ErrCommandAmbiguous", err)
	}
}

func TestMaliciousProcessPathCannotAffectResolution(t *testing.T) {
	malicious := t.TempDir()
	writeResolverExecutable(t, malicious, "nginx", 0o755)
	t.Setenv("PATH", malicious)
	_, err := resolveTestExecutable("nginx", nil, nil)
	if !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("error=%v, want ErrCommandNotFound", err)
	}
}

func secureResolverTestRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(".", ".setpoint-trusted-root-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(absolute, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(absolute); err != nil {
			t.Errorf("remove resolver test root: %v", err)
		}
	})
	return absolute
}

func writeResolverExecutable(t *testing.T, root, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func symlinkResolverFile(t *testing.T, target, entry string) {
	t.Helper()
	if err := os.Symlink(target, entry); err != nil {
		t.Fatal(err)
	}
}

func approvedNodeRoot(path string) trustedexec.Root {
	return trustedexec.Root{Path: path, Scope: trustedexec.ScopeNode, Source: "node:test"}
}

func resolveTestExecutable(name string, builtIn []string, approved []trustedexec.Root) (string, error) {
	return resolveSecureExecutableOwnedBy(name, builtIn, approved, uint32(os.Geteuid()))
}
