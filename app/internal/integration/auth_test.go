package integration

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/auth"
	storage "setpoint/internal/storage/sqlite"
)

func provisionAgentCredential(t *testing.T, store *storage.Store, client *agent.Client, agentID string) {
	t.Helper()
	now := time.Now().UTC()
	enrollment, err := auth.Generate(auth.EnrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnrollmentToken(context.Background(), auth.EnrollmentRecord{
		ID: enrollment.ID, Digest: enrollment.Digest, ExpiresAt: now.Add(time.Minute), MaxUses: 1, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create test enrollment token: %v", err)
	}
	credential, err := auth.Generate(auth.AgentCredential)
	if err != nil {
		t.Fatal(err)
	}
	presented, err := auth.Parse(auth.EnrollmentToken, enrollment.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollAgentCredential(context.Background(), presented, auth.CredentialRecord{
		ID: credential.ID, AgentID: agentID, Digest: credential.Digest, CreatedAt: now,
	}, now); err != nil {
		t.Fatalf("provision test Agent credential: %v", err)
	}
	client.SetCredential(credential.Secret)
}
