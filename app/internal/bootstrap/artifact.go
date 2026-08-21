package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type DirectoryArtifactProvider struct {
	directory string
	version   string
}

func NewDirectoryArtifactProvider(directory, version string) (*DirectoryArtifactProvider, error) {
	directory = strings.TrimSpace(directory)
	version = strings.TrimSpace(version)
	if directory == "" || version == "" {
		return nil, fmt.Errorf("Agent artifact directory and version are required")
	}
	return &DirectoryArtifactProvider{directory: directory, version: version}, nil
}

func (provider *DirectoryArtifactProvider) Select(_ context.Context, goos, arch string) (Artifact, error) {
	goos = strings.TrimSpace(strings.ToLower(goos))
	arch = normalizeArch(arch)
	if goos != "linux" || (arch != "amd64" && arch != "arm64") {
		return Artifact{}, &Error{Code: ErrorUnsupportedArch, Message: fmt.Sprintf("no approved Setpoint Agent artifact for %s/%s", goos, arch)}
	}
	artifactPath := filepath.Join(provider.directory, "setpoint-agent-linux-"+arch)
	file, err := os.Open(artifactPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Artifact{}, &Error{Code: ErrorArtifactNotFound, Message: "approved Setpoint Agent artifact was not found", Err: err}
		}
		return Artifact{}, fmt.Errorf("open Agent artifact %s/%s: %w", goos, arch, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return Artifact{}, fmt.Errorf("hash Agent artifact %s/%s: %w", goos, arch, err)
	}
	return Artifact{
		OS: goos, Arch: arch, Version: provider.version,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Path: artifactPath,
	}, nil
}

func normalizeArch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}
