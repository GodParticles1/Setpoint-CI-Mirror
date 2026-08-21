package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"setpoint/internal/auth"
	"setpoint/internal/protocol"
)

type StoredCredential struct {
	CredentialID string    `json:"credential_id"`
	Secret       string    `json:"secret"`
	CreatedAt    time.Time `json:"created_at"`
}

type EnrollmentClient interface {
	Enroll(context.Context, string, protocol.EnrollmentRequest) (protocol.AgentCredentialResponse, error)
	SetCredential(string)
}

func BootstrapCredential(ctx context.Context, config Config, client EnrollmentClient, agentID string) (StoredCredential, bool, error) {
	credential, found, err := LoadCredential(config.CredentialPath)
	if err != nil {
		return StoredCredential{}, false, err
	}
	if found {
		client.SetCredential(credential.Secret)
		return credential, false, nil
	}
	token := strings.TrimSpace(config.EnrollmentToken)
	if token != "" && strings.TrimSpace(config.EnrollmentTokenFile) != "" {
		return StoredCredential{}, false, errors.New("Agent enrollment token and enrollment token file are mutually exclusive")
	}
	if token == "" && strings.TrimSpace(config.EnrollmentTokenFile) != "" {
		fileToken, err := consumeEnrollmentTokenFile(config.EnrollmentTokenFile)
		if err != nil {
			return StoredCredential{}, false, err
		}
		token = fileToken
	}
	if token == "" {
		return StoredCredential{}, false, errors.New("Agent is not enrolled; a one-time enrollment token source is required")
	}
	response, err := client.Enroll(ctx, token, protocol.EnrollmentRequest{AgentID: agentID})
	if err != nil {
		return StoredCredential{}, false, fmt.Errorf("enroll Agent: %w", err)
	}
	credential = StoredCredential{
		CredentialID: response.CredentialID, Secret: response.Secret, CreatedAt: response.CreatedAt,
	}
	if err := SaveCredential(config.CredentialPath, credential); err != nil {
		return StoredCredential{}, false, fmt.Errorf("persist Agent credential: %w", err)
	}
	client.SetCredential(credential.Secret)
	return credential, true, nil
}

func LoadCredential(path string) (StoredCredential, bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return StoredCredential{}, false, nil
	}
	if err != nil {
		return StoredCredential{}, false, fmt.Errorf("read Agent credential: %w", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			return StoredCredential{}, false, fmt.Errorf("inspect Agent credential permissions: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return StoredCredential{}, false, fmt.Errorf("Agent credential permissions %04o expose secret data", info.Mode().Perm())
		}
	}
	var credential StoredCredential
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return StoredCredential{}, false, fmt.Errorf("decode Agent credential: %w", err)
	}
	if err := validateStoredCredential(credential); err != nil {
		return StoredCredential{}, false, err
	}
	return credential, true, nil
}

func SaveCredential(path string, credential StoredCredential) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("Agent credential path is required")
	}
	if err := validateStoredCredential(credential); err != nil {
		return err
	}
	contents, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode Agent credential: %w", err)
	}
	contents = append(contents, '\n')
	directory := filepath.Dir(filepath.Clean(path))
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create Agent credential directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".agent-credential-*")
	if err != nil {
		return fmt.Errorf("create temporary Agent credential: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("restrict Agent credential permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Agent credential: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Agent credential: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Agent credential: %w", err)
	}
	if err := replaceCredentialFile(temporaryPath, filepath.Clean(path)); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync Agent credential directory: %w", err)
	}
	return nil
}

func replaceCredentialFile(temporaryPath, targetPath string) error {
	if err := os.Rename(temporaryPath, targetPath); err == nil {
		return nil
	} else if _, statErr := os.Stat(targetPath); statErr != nil {
		return fmt.Errorf("install Agent credential: %w", err)
	}
	backupPath := targetPath + ".previous"
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale Agent credential backup: %w", err)
	}
	if err := os.Rename(targetPath, backupPath); err != nil {
		return fmt.Errorf("stage existing Agent credential: %w", err)
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		if restoreErr := os.Rename(backupPath, targetPath); restoreErr != nil {
			return fmt.Errorf("install Agent credential: %v; restore previous credential: %w", err, restoreErr)
		}
		return fmt.Errorf("install Agent credential: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("remove previous Agent credential: %w", err)
	}
	return nil
}

func validateStoredCredential(credential StoredCredential) error {
	presented, err := auth.Parse(auth.AgentCredential, credential.Secret)
	if err != nil {
		return fmt.Errorf("validate Agent credential secret: %w", err)
	}
	if credential.CredentialID == "" || presented.ID != credential.CredentialID {
		return errors.New("Agent credential ID does not match its secret")
	}
	if credential.CreatedAt.IsZero() {
		return errors.New("Agent credential creation time is required")
	}
	return nil
}
