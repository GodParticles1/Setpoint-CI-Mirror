package protocol

import (
	"encoding/json"

	"setpoint/internal/checkrun"
)

type CreateSiteRequest struct {
	APIVersion string             `json:"api_version"`
	Kind       string             `json:"kind"`
	Metadata   CreateSiteMetadata `json:"metadata"`
	Spec       SiteSpec           `json:"spec"`
}

type CreateSiteMetadata struct {
	IdempotencyKey string `json:"idempotency_key"`
}

type SiteSpec struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description"`
	TrustedExecutableRoots []string `json:"trusted_executable_roots,omitempty"`
}

type UpdateSiteRequest struct {
	Spec SiteSpec `json:"spec"`
}

type UpdateNodeRequest struct {
	SiteID                 *string   `json:"site_id,omitempty"`
	Tags                   *[]string `json:"tags,omitempty"`
	Notes                  *string   `json:"notes,omitempty"`
	TrustedExecutableRoots *[]string `json:"trusted_executable_roots,omitempty"`
}

type CreateCheckRunRequest struct {
	APIVersion string                 `json:"api_version"`
	Kind       string                 `json:"kind"`
	Metadata   CreateCheckRunMetadata `json:"metadata"`
	Spec       CreateCheckRunSpec     `json:"spec"`
}

type CreateCheckRunMetadata struct {
	IdempotencyKey string `json:"idempotency_key"`
	Name           string `json:"name,omitempty"`
}

type CreateCheckRunSpec struct {
	NodeIDs    []string                   `json:"node_ids"`
	CheckIDs   []string                   `json:"check_ids,omitempty"`
	BundleIDs  []string                   `json:"bundle_ids,omitempty"`
	PolicyIDs  []string                   `json:"policy_ids,omitempty"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

type CheckRunListResponse struct {
	Runs   []checkrun.Resource `json:"runs"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

type ListOptions struct {
	Limit  int
	Offset int
}
