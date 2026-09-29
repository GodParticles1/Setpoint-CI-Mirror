package bootstrap

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPackagedAgentDistribution(t *testing.T) {
	directory := os.Getenv("SETPOINT_TEST_AGENT_DISTRIBUTION_DIR")
	version := os.Getenv("SETPOINT_TEST_AGENT_DISTRIBUTION_VERSION")
	if directory == "" || version == "" {
		t.Skip("formal Agent distribution is produced by the packaging gate")
	}

	versionBytes, err := os.ReadFile(filepath.Join(directory, "VERSION"))
	if err != nil {
		t.Fatalf("read packaged VERSION: %v", err)
	}
	if got := strings.TrimSpace(string(versionBytes)); got != version {
		t.Fatalf("packaged version = %q, want %q", got, version)
	}

	sumsFile, err := os.Open(filepath.Join(directory, "SHA256SUMS"))
	if err != nil {
		t.Fatalf("open packaged SHA256SUMS: %v", err)
	}
	defer sumsFile.Close()
	expected := map[string]string{}
	scanner := bufio.NewScanner(sumsFile)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			t.Fatalf("invalid SHA256SUMS row %q", scanner.Text())
		}
		expected[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SHA256SUMS: %v", err)
	}

	for _, name := range []string{"VERSION", "SOURCE_SHA"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "SOURCE_SHA" && !regexp.MustCompile(`^[0-9a-f]{40}\n$`).Match(data) {
			t.Fatal("invalid packaged source SHA")
		}
		sum := sha256.Sum256(data)
		if expected[name] != hex.EncodeToString(sum[:]) {
			t.Fatalf("provenance checksum mismatch: %s", name)
		}
	}
	provider, err := NewDirectoryArtifactProvider(directory, version)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "setpoint-agent-linux-" + arch
		artifact, err := provider.Select(context.Background(), "linux", arch)
		if err != nil {
			t.Fatalf("select %s: %v", arch, err)
		}
		if artifact.OS != "linux" || artifact.Arch != arch || artifact.Version != version {
			t.Fatalf("unexpected packaged artifact: %#v", artifact)
		}
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Fatalf("packaged artifact missing: %v", err)
		}
		if want := expected[name]; want == "" || artifact.SHA256 != want {
			t.Fatalf("%s SHA256 = %q, manifest = %q", name, artifact.SHA256, want)
		}
	}
}
