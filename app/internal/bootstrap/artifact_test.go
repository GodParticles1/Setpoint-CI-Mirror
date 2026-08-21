package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryArtifactProviderSelectsApprovedArchitectures(t *testing.T) {
	directory := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		if err := os.WriteFile(filepath.Join(directory, "setpoint-agent-linux-"+arch), []byte("agent-"+arch), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	provider, err := NewDirectoryArtifactProvider(directory, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "x86_64", "arm64", "aarch64"} {
		artifact, err := provider.Select(context.Background(), "linux", arch)
		if err != nil {
			t.Fatalf("select %s: %v", arch, err)
		}
		if artifact.Version != "v-test" || artifact.SHA256 == "" || artifact.Path == "" {
			t.Fatalf("incomplete artifact: %#v", artifact)
		}
	}
}

func TestDirectoryArtifactProviderRejectsUnsupportedArchitecture(t *testing.T) {
	provider, err := NewDirectoryArtifactProvider(t.TempDir(), "v-test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Select(context.Background(), "linux", "arm")
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorUnsupportedArch {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDirectoryArtifactProviderReturnsTypedMissingArtifact(t *testing.T) {
	provider, err := NewDirectoryArtifactProvider(t.TempDir(), "v-test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Select(context.Background(), "linux", "amd64")
	assertBootstrapErrorCode(t, err, ErrorArtifactNotFound)
}
