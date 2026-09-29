package buildinfo_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVersionCLIReportsInjectedIdentityWithoutStartup(t *testing.T) {
	const version = "v1.2.3-rc.4"
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, component := range []string{"server", "agent"} {
		t.Run(component, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "setpoint-"+component)
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-X setpoint/internal/buildinfo.Version="+version+" -X setpoint/internal/buildinfo.SourceSHA="+sha, "-o", binary, "../../cmd/setpoint-"+component)
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v %s", err, out)
			}
			cmd := exec.CommandContext(ctx, binary, "--config", filepath.Join(dir, "does-not-exist.json"), "--version")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("version: %v %s", err, out)
			}
			want := "setpoint-" + component + " version=" + version + " source_sha=" + sha
			if strings.TrimSpace(string(out)) != want {
				t.Fatalf("got %q want %q", out, want)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("version created runtime files: %v", entries)
			}
		})
	}
}
