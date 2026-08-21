package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/auth"
	"setpoint/internal/protocol"
)

const (
	defaultEnrollmentLifetime = 10 * time.Minute
	maximumEnrollmentLifetime = 24 * time.Hour
	maximumEnrollmentUses     = 100
)

type AuthRepository interface {
	CreateEnrollmentToken(context.Context, auth.EnrollmentRecord) error
	EnrollAgentCredential(context.Context, auth.PresentedToken, auth.CredentialRecord, time.Time) error
	AuthenticateAgentCredential(context.Context, auth.PresentedToken, string, time.Time) (auth.Credential, error)
	RotateAgentCredential(context.Context, auth.PresentedToken, string, auth.CredentialRecord, time.Time) error
	RevokeEnrollmentToken(context.Context, string, time.Time) error
	RevokeAgentCredential(context.Context, string, time.Time) error
}

func (service *Service) CreateEnrollmentToken(
	ctx context.Context,
	request protocol.CreateEnrollmentTokenRequest,
) (protocol.EnrollmentTokenResponse, error) {
	lifetime, maxUses, err := validateEnrollmentTokenRequest(request)
	if err != nil {
		return protocol.EnrollmentTokenResponse{}, &ValidationError{Err: err}
	}
	generated, err := auth.Generate(auth.EnrollmentToken)
	if err != nil {
		return protocol.EnrollmentTokenResponse{}, err
	}
	createdAt := service.now().UTC()
	expiresAt := createdAt.Add(lifetime)
	if err := service.nodes.CreateEnrollmentToken(ctx, auth.EnrollmentRecord{
		ID: generated.ID, Digest: generated.Digest, ExpiresAt: expiresAt,
		MaxUses: maxUses, CreatedAt: createdAt,
	}); err != nil {
		return protocol.EnrollmentTokenResponse{}, err
	}
	return protocol.EnrollmentTokenResponse{
		APIVersion: "setpoint.io/v1", Kind: "EnrollmentToken",
		Metadata: protocol.ResourceID{ID: generated.ID},
		Status:   protocol.EnrollmentTokenStatus{ExpiresAt: expiresAt, MaxUses: maxUses},
		Secret:   generated.Secret,
	}, nil
}

func (service *Service) EnrollAgent(
	ctx context.Context,
	authorization string,
	request protocol.EnrollmentRequest,
) (protocol.AgentCredentialResponse, error) {
	agentID := strings.TrimSpace(request.AgentID)
	if err := validateIdentifier(agentID); err != nil {
		return protocol.AgentCredentialResponse{}, &ValidationError{Err: fmt.Errorf("agent_id: %w", err)}
	}
	presented, err := auth.ParseBearer(authorization, auth.EnrollmentToken)
	if err != nil {
		return protocol.AgentCredentialResponse{}, err
	}
	generated, err := auth.Generate(auth.AgentCredential)
	if err != nil {
		return protocol.AgentCredentialResponse{}, err
	}
	createdAt := service.now().UTC()
	if err := service.nodes.EnrollAgentCredential(ctx, presented, auth.CredentialRecord{
		ID: generated.ID, AgentID: agentID, Digest: generated.Digest, CreatedAt: createdAt,
	}, createdAt); err != nil {
		return protocol.AgentCredentialResponse{}, err
	}
	return protocol.AgentCredentialResponse{
		AgentID: agentID, CredentialID: generated.ID, Secret: generated.Secret, CreatedAt: createdAt,
	}, nil
}

func (service *Service) AuthenticateAgent(
	ctx context.Context,
	authorization string,
	agentID string,
) error {
	agentID = strings.TrimSpace(agentID)
	if err := validateIdentifier(agentID); err != nil {
		return &ValidationError{Err: fmt.Errorf("agent_id: %w", err)}
	}
	presented, err := auth.ParseBearer(authorization, auth.AgentCredential)
	if err != nil {
		return err
	}
	_, err = service.nodes.AuthenticateAgentCredential(ctx, presented, agentID, service.now().UTC())
	return err
}

func (service *Service) RotateAgentCredential(
	ctx context.Context,
	authorization string,
	agentID string,
) (protocol.AgentCredentialResponse, error) {
	agentID = strings.TrimSpace(agentID)
	if err := validateIdentifier(agentID); err != nil {
		return protocol.AgentCredentialResponse{}, &ValidationError{Err: fmt.Errorf("agent_id: %w", err)}
	}
	presented, err := auth.ParseBearer(authorization, auth.AgentCredential)
	if err != nil {
		return protocol.AgentCredentialResponse{}, err
	}
	generated, err := auth.Generate(auth.AgentCredential)
	if err != nil {
		return protocol.AgentCredentialResponse{}, err
	}
	createdAt := service.now().UTC()
	if err := service.nodes.RotateAgentCredential(ctx, presented, agentID, auth.CredentialRecord{
		ID: generated.ID, AgentID: agentID, Digest: generated.Digest, CreatedAt: createdAt, RotatedFrom: presented.ID,
	}, createdAt); err != nil {
		return protocol.AgentCredentialResponse{}, err
	}
	return protocol.AgentCredentialResponse{
		AgentID: agentID, CredentialID: generated.ID, Secret: generated.Secret, CreatedAt: createdAt,
	}, nil
}

func (service *Service) RevokeEnrollmentToken(ctx context.Context, id string) (protocol.RevocationResponse, error) {
	return service.revoke(ctx, id, service.nodes.RevokeEnrollmentToken)
}

func (service *Service) RevokeAgentCredential(ctx context.Context, id string) (protocol.RevocationResponse, error) {
	return service.revoke(ctx, id, service.nodes.RevokeAgentCredential)
}

func (service *Service) revoke(
	ctx context.Context,
	id string,
	revoke func(context.Context, string, time.Time) error,
) (protocol.RevocationResponse, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return protocol.RevocationResponse{}, &ValidationError{Err: fmt.Errorf("resource id: %w", err)}
	}
	revokedAt := service.now().UTC()
	if err := revoke(ctx, id, revokedAt); err != nil {
		return protocol.RevocationResponse{}, err
	}
	return protocol.RevocationResponse{ID: id, RevokedAt: revokedAt}, nil
}

func validateEnrollmentTokenRequest(request protocol.CreateEnrollmentTokenRequest) (time.Duration, int, error) {
	if request.APIVersion != "setpoint.io/v1" {
		return 0, 0, errors.New("api_version must be setpoint.io/v1")
	}
	if request.Kind != "EnrollmentToken" {
		return 0, 0, errors.New("kind must be EnrollmentToken")
	}
	lifetime := defaultEnrollmentLifetime
	if request.Spec.ExpiresIn != "" {
		parsed, err := time.ParseDuration(request.Spec.ExpiresIn)
		if err != nil {
			return 0, 0, fmt.Errorf("spec.expires_in: %w", err)
		}
		lifetime = parsed
	}
	if lifetime <= 0 || lifetime > maximumEnrollmentLifetime {
		return 0, 0, errors.New("spec.expires_in must be positive and at most 24h")
	}
	maxUses := request.Spec.MaxUses
	if maxUses == 0 {
		maxUses = 1
	}
	if maxUses < 1 || maxUses > maximumEnrollmentUses {
		return 0, 0, errors.New("spec.max_uses must be between 1 and 100")
	}
	return lifetime, maxUses, nil
}
