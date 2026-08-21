package agent

import (
	"context"
	"errors"
	"net/url"

	"setpoint/internal/auth"
	"setpoint/internal/protocol"
)

func (client *Client) SetCredential(secret string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.credential = secret
}

func (client *Client) Credential() string {
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client.credential
}

func (client *Client) Enroll(
	ctx context.Context,
	enrollmentToken string,
	request protocol.EnrollmentRequest,
) (protocol.AgentCredentialResponse, error) {
	var response protocol.AgentCredentialResponse
	err := client.post(ctx, "/api/v1/agents/enroll", request, &response, enrollmentToken)
	return response, err
}

func (client *Client) RotateCredential(ctx context.Context, agentID string) (protocol.AgentCredentialResponse, error) {
	var response protocol.AgentCredentialResponse
	err := client.post(ctx, "/api/v1/agents/"+url.PathEscape(agentID)+"/credentials/rotate", nil, &response, client.Credential())
	return response, err
}

func IsPermanentAuthenticationError(err error) bool {
	var apiError *APIError
	if !errors.As(err, &apiError) {
		return false
	}
	switch auth.ErrorCode(apiError.Code) {
	case auth.CodeMissing, auth.CodeMalformed, auth.CodeInvalid, auth.CodeExpired,
		auth.CodeRevoked, auth.CodeAgentMismatch,
		auth.CodeEnrollmentTokenExpired, auth.CodeEnrollmentTokenRevoked, auth.CodeEnrollmentTokenExhausted:
		return true
	default:
		return false
	}
}
