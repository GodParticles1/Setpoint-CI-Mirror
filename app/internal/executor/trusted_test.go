package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedExecutableResolutionIgnoresProcessPath(t *testing.T) {
	directory := t.TempDir()
	name := "setpoint-executor-test"
	candidate := filepath.Join(directory, name)
	if err := os.WriteFile(candidate, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	execution := &OSExecutor{OutputLimit: 1024}
	if _, err := execution.resolveExecutable(name); err == nil {
		t.Fatal("process PATH was used to resolve an executable")
	}
}

func TestTrustedExecutableResolutionRejectsRelativePaths(t *testing.T) {
	execution := &OSExecutor{OutputLimit: 1024, trustedDirectories: []string{t.TempDir()}}
	for _, name := range []string{"./tool", "..\\tool"} {
		if _, err := execution.resolveExecutable(name); err == nil {
			t.Fatalf("relative path %q was accepted", name)
		}
	}
}
