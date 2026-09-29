package buildinfo_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReleasePackageProvenance(t *testing.T) {
	dir := os.Getenv("SETPOINT_TEST_RELEASE_DIR")
	if dir == "" {
		t.Skip("release package supplied by packaging gate")
	}
	version, sha := os.Getenv("SETPOINT_TEST_RELEASE_VERSION"), os.Getenv("SETPOINT_TEST_RELEASE_SHA")
	if version == "" || len(sha) != 40 {
		t.Fatal("expected package version and exact SHA required")
	}
	for _, prefix := range []string{"", "agents"} {
		for name, want := range map[string]string{"VERSION": version, "SOURCE_SHA": sha} {
			data, err := os.ReadFile(filepath.Join(dir, prefix, name))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != want+"\n" {
				t.Fatalf("%s/%s identity mismatch", prefix, name)
			}
		}
	}
	files := []string{"setpoint-server.exe", "setpoint-agent.exe", "start.bat", "stop.bat", "start.ps1", "stop.ps1", "VERSION", "SOURCE_SHA", "agents/VERSION", "agents/SOURCE_SHA", "agents/SHA256SUMS", "agents/setpoint-agent-linux-amd64", "agents/setpoint-agent-linux-arm64"}
	verifyManifest(t, dir, "SHA256SUMS", files)
	verifyManifest(t, filepath.Join(dir, "agents"), "SHA256SUMS", []string{"setpoint-agent-linux-amd64", "setpoint-agent-linux-arm64", "VERSION", "SOURCE_SHA"})
	for _, name := range []string{"setpoint-server.exe", "setpoint-agent.exe", "agents/setpoint-agent-linux-amd64", "agents/setpoint-agent-linux-arm64"} {
		path := filepath.Join(dir, name)
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		settings := map[string]string{}
		for _, entry := range info.Settings {
			settings[entry.Key] = entry.Value
		}
		wantOS, wantArch := "windows", "amd64"
		if strings.Contains(name, "linux") {
			wantOS = "linux"
		}
		if strings.HasSuffix(name, "arm64") {
			wantArch = "arm64"
		}
		if settings["GOOS"] != wantOS || settings["GOARCH"] != wantArch {
			t.Fatalf("wrong artifact platform: %s %+v", name, settings)
		}
		// -trimpath intentionally omits linker flags from Go's build settings.
		// Check the injected string data in every cross-built artifact, and run
		// the native artifact below to verify the observable CLI contract.
		binary, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{version, sha} {
			if !bytes.Contains(binary, append([]byte(value), 0)) {
				t.Fatalf("%s lacks embedded identity value %q", name, value)
			}
		}

		if runtime.GOOS == wantOS && runtime.GOARCH == wantArch {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			cmd := exec.CommandContext(ctx, path, "--config", filepath.Join(t.TempDir(), "missing.json"), "--version")
			cmd.Dir = t.TempDir()
			out, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("%s --version: %v %s", name, err, out)
			}
			component := "setpoint-agent"
			if strings.Contains(name, "server") {
				component = "setpoint-server"
			}
			if strings.TrimSpace(string(out)) != component+" version="+version+" source_sha="+sha {
				t.Fatalf("%s identity output=%q", name, out)
			}
			t.Logf("%s: %s", name, strings.TrimSpace(string(out)))
		}
	}
}

func verifyManifest(t *testing.T, dir, name string, expected []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	sums := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			t.Fatalf("invalid checksum row %q", line)
		}
		file := strings.TrimPrefix(fields[1], "*")
		if _, duplicate := sums[file]; duplicate {
			t.Fatal("duplicate checksum path")
		}
		sums[file] = fields[0]
	}
	if len(sums) != len(expected) {
		t.Fatalf("manifest entries=%d want=%d", len(sums), len(expected))
	}
	for _, file := range expected {
		contents, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(contents)
		if sums[file] != hex.EncodeToString(hash[:]) {
			t.Fatalf("checksum mismatch: %s", file)
		}
	}
}
