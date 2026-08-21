package auth

import "time"

type EnrollmentRecord struct {
	ID        string
	Digest    []byte
	ExpiresAt time.Time
	MaxUses   int
	CreatedAt time.Time
}

type CredentialRecord struct {
	ID          string
	AgentID     string
	Digest      []byte
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	RotatedFrom string
}

type Credential struct {
	ID          string
	AgentID     string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	RotatedFrom string
}
