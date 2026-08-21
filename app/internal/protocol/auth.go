package protocol

import "time"

type CreateEnrollmentTokenRequest struct {
	APIVersion string                    `json:"api_version"`
	Kind       string                    `json:"kind"`
	Spec       CreateEnrollmentTokenSpec `json:"spec"`
}

type CreateEnrollmentTokenSpec struct {
	ExpiresIn string `json:"expires_in,omitempty"`
	MaxUses   int    `json:"max_uses,omitempty"`
}

type EnrollmentTokenResponse struct {
	APIVersion string                `json:"api_version"`
	Kind       string                `json:"kind"`
	Metadata   ResourceID            `json:"metadata"`
	Status     EnrollmentTokenStatus `json:"status"`
	Secret     string                `json:"secret"`
}

type EnrollmentTokenStatus struct {
	ExpiresAt time.Time `json:"expires_at"`
	MaxUses   int       `json:"max_uses"`
}

type ResourceID struct {
	ID string `json:"id"`
}

type EnrollmentRequest struct {
	AgentID string `json:"agent_id"`
}

type AgentCredentialResponse struct {
	AgentID      string    `json:"agent_id"`
	CredentialID string    `json:"credential_id"`
	Secret       string    `json:"secret"`
	CreatedAt    time.Time `json:"created_at"`
}

type RevocationResponse struct {
	ID        string    `json:"id"`
	RevokedAt time.Time `json:"revoked_at"`
}
