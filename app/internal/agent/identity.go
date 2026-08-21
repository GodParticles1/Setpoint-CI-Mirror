package agent

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadOrCreateID(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err == nil {
		return parseID(contents)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent identity: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(path)), 0o700); err != nil {
		return "", fmt.Errorf("create agent identity directory: %w", err)
	}
	id, err := generateUUID()
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".agent-id-*")
	if err != nil {
		return "", fmt.Errorf("create temporary agent identity: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("restrict agent identity permissions: %w", err)
	}
	if _, err := temporary.WriteString(id + "\n"); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write agent identity: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("sync agent identity: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close agent identity: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("read concurrently created agent identity: %w", readErr)
			}
			return parseID(contents)
		}
		return "", fmt.Errorf("install agent identity: %w", err)
	}
	return id, nil
}

func generateUUID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate agent identity: %w", err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func parseID(contents []byte) (string, error) {
	id := strings.TrimSpace(string(contents))
	if !validUUID(id) {
		return "", errors.New("agent identity file does not contain a valid UUID")
	}
	return id, nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
