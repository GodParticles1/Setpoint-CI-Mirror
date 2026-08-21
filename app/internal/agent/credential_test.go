package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/auth"
	"setpoint/internal/protocol"
)

func TestCredentialFileRoundTripAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "credential.json")
	first := generatedStoredCredential(t, time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC))
	if err := SaveCredential(path, first); err != nil {
		t.Fatalf("save first credential: %v", err)
	}
	loaded, found, err := LoadCredential(path)
	if err != nil || !found || loaded != first {
		t.Fatal("first credential did not round-trip")
	}
	second := generatedStoredCredential(t, first.CreatedAt.Add(time.Minute))
	if err := SaveCredential(path, second); err != nil {
		t.Fatalf("replace credential: %v", err)
	}
	loaded, found, err = LoadCredential(path)
	if err != nil || !found || loaded != second {
		t.Fatal("replacement credential did not round-trip")
	}
	if _, err := os.Stat(path + ".previous"); !os.IsNotExist(err) {
		t.Fatalf("credential backup residue exists: %v", err)
	}
}

func TestLoadCredentialRejectsMismatchedID(t *testing.T) {
	credential := generatedStoredCredential(t, time.Now().UTC())
	credential.CredentialID = "wrong"
	if err := SaveCredential(filepath.Join(t.TempDir(), "credential.json"), credential); err == nil {
		t.Fatal("mismatched credential was saved")
	}
}

func TestBootstrapCredentialEnrollsOnceThenLoadsPersistedSecret(t *testing.T) {
	config := DefaultConfig()
	config.CredentialPath = filepath.Join(t.TempDir(), "credential.json")
	config.EnrollmentToken = "one-time-token"
	remote := &fakeEnrollmentClient{response: newCredentialResponse(t)}
	first, newlyEnrolled, err := BootstrapCredential(context.Background(), config, remote, "agent-1")
	if err != nil {
		t.Fatalf("bootstrap first credential: %v", err)
	}
	if remote.enrollCalls != 1 || remote.current != first.Secret {
		t.Fatalf("first bootstrap calls=%d current=%q", remote.enrollCalls, remote.current)
	}
	if !newlyEnrolled {
		t.Fatal("fresh enrollment did not report newlyEnrolled")
	}
	config.EnrollmentToken = ""
	secondRemote := &fakeEnrollmentClient{}
	second, newlyEnrolled, err := BootstrapCredential(context.Background(), config, secondRemote, "agent-1")
	if err != nil {
		t.Fatalf("bootstrap persisted credential: %v", err)
	}
	if second != first || secondRemote.enrollCalls != 0 || secondRemote.current != first.Secret {
		t.Fatal("persisted credential was not reused without enrollment")
	}
	if newlyEnrolled {
		t.Fatal("persisted credential incorrectly reported newlyEnrolled")
	}
}

func TestBootstrapCredentialRestartIgnoresConsumedTokenFileWhenCredentialExists(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "bootstrap", "enrollment-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("one-time-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.CredentialPath = filepath.Join(directory, "credential.json")
	config.EnrollmentTokenFile = tokenPath
	remote := &fakeEnrollmentClient{response: newCredentialResponse(t)}
	first, newlyEnrolled, err := BootstrapCredential(context.Background(), config, remote, "agent-1")
	if err != nil || !newlyEnrolled || remote.enrollCalls != 1 {
		t.Fatalf("initial file-token enrollment failed: newly=%v calls=%d err=%v", newlyEnrolled, remote.enrollCalls, err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Fatalf("token file was not consumed: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		restarted := &fakeEnrollmentClient{}
		credential, newlyEnrolled, err := BootstrapCredential(context.Background(), config, restarted, "agent-1")
		if err != nil || newlyEnrolled || restarted.enrollCalls != 0 || credential != first {
			t.Fatalf("restart %d did not reuse credential: credential=%#v newly=%v calls=%d err=%v", attempt+1, credential, newlyEnrolled, restarted.enrollCalls, err)
		}
	}
}

func TestBootstrapCredentialMissingCredentialAndTokenFailsClosed(t *testing.T) {
	config := DefaultConfig()
	config.CredentialPath = filepath.Join(t.TempDir(), "missing-credential.json")
	if _, _, err := BootstrapCredential(context.Background(), config, &fakeEnrollmentClient{}, "agent-1"); err == nil {
		t.Fatal("missing credential and token source was accepted")
	}
}

func TestBootstrapCredentialExistingCredentialDoesNotRequireMissingTokenFile(t *testing.T) {
	config := DefaultConfig()
	config.CredentialPath = filepath.Join(t.TempDir(), "credential.json")
	first := generatedStoredCredential(t, time.Now().UTC())
	if err := SaveCredential(config.CredentialPath, first); err != nil {
		t.Fatal(err)
	}
	config.EnrollmentTokenFile = filepath.Join(t.TempDir(), "already-consumed-token")
	remote := &fakeEnrollmentClient{}
	credential, newlyEnrolled, err := BootstrapCredential(context.Background(), config, remote, "agent-1")
	if err != nil || newlyEnrolled || remote.enrollCalls != 0 || credential != first {
		t.Fatalf("existing credential did not bypass missing token: credential=%#v newly=%v calls=%d err=%v", credential, newlyEnrolled, remote.enrollCalls, err)
	}
}

type fakeEnrollmentClient struct {
	response    protocol.AgentCredentialResponse
	enrollCalls int
	current     string
}

func (client *fakeEnrollmentClient) Enroll(context.Context, string, protocol.EnrollmentRequest) (protocol.AgentCredentialResponse, error) {
	client.enrollCalls++
	return client.response, nil
}

func (client *fakeEnrollmentClient) SetCredential(secret string) {
	client.current = secret
}

func generatedStoredCredential(t *testing.T, createdAt time.Time) StoredCredential {
	t.Helper()
	generated, err := auth.Generate(auth.AgentCredential)
	if err != nil {
		t.Fatal(err)
	}
	return StoredCredential{CredentialID: generated.ID, Secret: generated.Secret, CreatedAt: createdAt}
}

func newCredentialResponse(t *testing.T) protocol.AgentCredentialResponse {
	t.Helper()
	stored := generatedStoredCredential(t, time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC))
	return protocol.AgentCredentialResponse{
		AgentID: "agent-1", CredentialID: stored.CredentialID, Secret: stored.Secret, CreatedAt: stored.CreatedAt,
	}
}
