package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"setpoint/internal/auth"
	"setpoint/internal/domain"
)

func TestEnrollmentCredentialRotationAndRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC)
	store, err := open(ctx, filepath.Join(t.TempDir(), "setpoint.db"), func() time.Time { return now })
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	enrollment := mustToken(t, auth.EnrollmentToken)
	if err := store.CreateEnrollmentToken(ctx, auth.EnrollmentRecord{
		ID: enrollment.ID, Digest: enrollment.Digest, ExpiresAt: now.Add(time.Minute), MaxUses: 1, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create enrollment token: %v", err)
	}
	credential := mustToken(t, auth.AgentCredential)
	if err := store.EnrollAgentCredential(ctx, mustParse(t, auth.EnrollmentToken, enrollment.Secret), auth.CredentialRecord{
		ID: credential.ID, AgentID: "agent-1", Digest: credential.Digest, CreatedAt: now,
	}, now); err != nil {
		t.Fatalf("enroll Agent: %v", err)
	}
	if err := store.EnrollAgentCredential(ctx, mustParse(t, auth.EnrollmentToken, enrollment.Secret), auth.CredentialRecord{
		ID: mustToken(t, auth.AgentCredential).ID, AgentID: "agent-2", Digest: credential.Digest, CreatedAt: now,
	}, now); authCode(err) != auth.CodeEnrollmentTokenExhausted {
		t.Fatalf("reused enrollment token error=%v", err)
	}

	presented := mustParse(t, auth.AgentCredential, credential.Secret)
	authenticated, err := store.AuthenticateAgentCredential(ctx, presented, "agent-1", now)
	if err != nil || authenticated.ID != credential.ID {
		t.Fatalf("authenticate credential: credential=%#v err=%v", authenticated, err)
	}
	if _, err := store.AuthenticateAgentCredential(ctx, presented, "other-agent", now); authCode(err) != auth.CodeAgentMismatch {
		t.Fatalf("Agent mismatch error=%v", err)
	}

	replacement := mustToken(t, auth.AgentCredential)
	wrongAgent := mustToken(t, auth.AgentCredential)
	if err := store.RotateAgentCredential(ctx, presented, "agent-1", auth.CredentialRecord{
		ID: wrongAgent.ID, AgentID: "agent-2", Digest: wrongAgent.Digest,
		CreatedAt: now.Add(time.Second), RotatedFrom: credential.ID,
	}, now.Add(time.Second)); err == nil {
		t.Fatal("rotation accepted a replacement for another Agent")
	}
	superseded := mustToken(t, auth.AgentCredential)
	if err := store.RotateAgentCredential(ctx, presented, "agent-1", auth.CredentialRecord{
		ID: superseded.ID, AgentID: "agent-1", Digest: superseded.Digest,
		CreatedAt: now.Add(time.Second), RotatedFrom: credential.ID,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("create pre-retry replacement: %v", err)
	}
	if err := store.RotateAgentCredential(ctx, presented, "agent-1", auth.CredentialRecord{
		ID: replacement.ID, AgentID: "agent-1", Digest: replacement.Digest, CreatedAt: now.Add(time.Second), RotatedFrom: credential.ID,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("rotate credential: %v", err)
	}
	if _, err := store.AuthenticateAgentCredential(ctx, presented, "agent-1", now.Add(2*time.Second)); err != nil {
		t.Fatalf("old credential did not survive an unactivated rotation: %v", err)
	}
	replacementPresented := mustParse(t, auth.AgentCredential, replacement.Secret)
	if _, err := store.AuthenticateAgentCredential(ctx, replacementPresented, "agent-1", now.Add(3*time.Second)); err != nil {
		t.Fatalf("authenticate replacement: %v", err)
	}
	supersededPresented := mustParse(t, auth.AgentCredential, superseded.Secret)
	if _, err := store.AuthenticateAgentCredential(ctx, supersededPresented, "agent-1", now.Add(4*time.Second)); authCode(err) != auth.CodeRevoked {
		t.Fatalf("activated replacement did not revoke superseded sibling: %v", err)
	}
	if _, err := store.AuthenticateAgentCredential(ctx, presented, "agent-1", now.Add(4*time.Second)); authCode(err) != auth.CodeRevoked {
		t.Fatalf("activated rotation did not revoke old credential: %v", err)
	}
	descendant := mustToken(t, auth.AgentCredential)
	if err := store.RotateAgentCredential(ctx, replacementPresented, "agent-1", auth.CredentialRecord{
		ID: descendant.ID, AgentID: "agent-1", Digest: descendant.Digest,
		CreatedAt: now.Add(5 * time.Second), RotatedFrom: replacement.ID,
	}, now.Add(5*time.Second)); err != nil {
		t.Fatalf("create descendant credential: %v", err)
	}
	if err := store.RevokeAgentCredential(ctx, replacement.ID, now.Add(5*time.Second)); err != nil {
		t.Fatalf("revoke replacement: %v", err)
	}
	if err := store.RevokeAgentCredential(ctx, replacement.ID, now.Add(4*time.Second)); err != nil {
		t.Fatalf("repeat credential revocation: %v", err)
	}
	if _, err := store.AuthenticateAgentCredential(ctx, replacementPresented, "agent-1", now.Add(5*time.Second)); authCode(err) != auth.CodeRevoked {
		t.Fatalf("revoked replacement error=%v", err)
	}
	descendantPresented := mustParse(t, auth.AgentCredential, descendant.Secret)
	if _, err := store.AuthenticateAgentCredential(ctx, descendantPresented, "agent-1", now.Add(7*time.Second)); authCode(err) != auth.CodeRevoked {
		t.Fatalf("descendant of explicitly revoked credential remains valid: %v", err)
	}
}

func TestEnrollmentTokenFailureCategories(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	store, err := Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, test := range []struct {
		name       string
		expiresAt  time.Time
		revoke     bool
		want       auth.ErrorCode
		useUnknown bool
	}{
		{name: "expired", expiresAt: now, want: auth.CodeEnrollmentTokenExpired},
		{name: "revoked", expiresAt: now.Add(time.Minute), revoke: true, want: auth.CodeEnrollmentTokenRevoked},
		{name: "unknown", expiresAt: now.Add(time.Minute), want: auth.CodeInvalid, useUnknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := mustToken(t, auth.EnrollmentToken)
			if !test.useUnknown {
				if err := store.CreateEnrollmentToken(ctx, auth.EnrollmentRecord{
					ID: token.ID, Digest: token.Digest, ExpiresAt: test.expiresAt, MaxUses: 1, CreatedAt: now,
				}); err != nil {
					t.Fatal(err)
				}
				if test.revoke {
					if err := store.RevokeEnrollmentToken(ctx, token.ID, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			credential := mustToken(t, auth.AgentCredential)
			err := store.EnrollAgentCredential(ctx, mustParse(t, auth.EnrollmentToken, token.Secret), auth.CredentialRecord{
				ID: credential.ID, AgentID: "agent", Digest: credential.Digest, CreatedAt: now,
			}, now)
			if authCode(err) != test.want {
				t.Fatalf("error=%v code=%q want=%q", err, authCode(err), test.want)
			}
		})
	}
	if err := store.RevokeEnrollmentToken(ctx, "missing", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoke missing token error=%v", err)
	}
}

func mustToken(t *testing.T, kind auth.TokenKind) auth.GeneratedToken {
	t.Helper()
	token, err := auth.Generate(kind)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func mustParse(t *testing.T, kind auth.TokenKind, secret string) auth.PresentedToken {
	t.Helper()
	presented, err := auth.Parse(kind, secret)
	if err != nil {
		t.Fatal(err)
	}
	return presented
}

func authCode(err error) auth.ErrorCode {
	var authError *auth.Error
	if errors.As(err, &authError) {
		return authError.Code
	}
	return ""
}
