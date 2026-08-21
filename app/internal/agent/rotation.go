package agent

import (
	"context"
	"fmt"

	"setpoint/internal/protocol"
)

func RotateAndPersistCredential(
	ctx context.Context,
	config Config,
	client *Client,
	registration protocol.RegistrationRequest,
) error {
	response, err := client.RotateCredential(ctx, registration.AgentID)
	if err != nil {
		return fmt.Errorf("rotate Agent credential: %w", err)
	}
	replacement := StoredCredential{
		CredentialID: response.CredentialID, Secret: response.Secret, CreatedAt: response.CreatedAt,
	}
	if err := SaveCredential(config.CredentialPath, replacement); err != nil {
		return fmt.Errorf("persist rotated Agent credential: %w", err)
	}
	client.SetCredential(replacement.Secret)
	if err := client.Register(ctx, registration); err != nil {
		return fmt.Errorf("activate persisted Agent credential; replacement remains saved for retry: %w", err)
	}
	return nil
}
