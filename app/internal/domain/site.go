package domain

import (
	"errors"
	"time"

	"setpoint/internal/trustedexec"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key is already used by a different resource")
	ErrSiteNameConflict    = errors.New("site name is already in use")
	ErrSiteNotEmpty        = errors.New("site still contains nodes")
)

type Site struct {
	ID                     string                       `json:"id"`
	Name                   string                       `json:"name"`
	Description            string                       `json:"description"`
	TrustedExecutableRoots []trustedexec.ConfiguredRoot `json:"trusted_executable_roots"`
	NodeCount              int                          `json:"node_count"`
	CreatedAt              time.Time                    `json:"created_at"`
	UpdatedAt              time.Time                    `json:"updated_at"`
}

type NodeUpdate struct {
	SiteID                 *string
	Tags                   *[]string
	Notes                  *string
	TrustedExecutableRoots *[]trustedexec.ConfiguredRoot
}
