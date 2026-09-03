package operation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrLockResourceBusy = errors.New("operation lock resource is already leased")

type LockResource struct {
	Key string `json:"key"`
}

type LockRequest struct {
	OwnerID   string         `json:"owner_id"`
	Resources []LockResource `json:"resources"`
	TTL       time.Duration  `json:"ttl"`
}

type LockLease struct {
	ID         string         `json:"id"`
	OwnerID    string         `json:"owner_id"`
	Resources  []LockResource `json:"resources"`
	AcquiredAt time.Time      `json:"acquired_at"`
	ExpiresAt  time.Time      `json:"expires_at"`
}

type LockManager interface {
	Acquire(context.Context, LockRequest) (LockLease, error)
	Renew(context.Context, LockLease, time.Duration) (LockLease, error)
	Release(context.Context, LockLease) error
}

// LeaseHandle exposes only the current lease snapshot to an Operation. The
// coordinator owns renewal and release; plugins may verify ownership/coverage
// immediately before a destructive checkpoint without managing the lease.
type LeaseHandle interface {
	Current() LockLease
	Validate(time.Time) error
}

func ResourceLockKey(target Target) (string, error) {
	if err := ValidateTarget(target); err != nil {
		return "", err
	}
	parts := []string{string(target.Kind), target.SiteID, target.NodeID, target.Component, target.Resource}
	return strings.Join(parts, "|"), nil
}

func NormalizeLockRequest(request LockRequest) (LockRequest, error) {
	request.OwnerID = strings.TrimSpace(request.OwnerID)
	if request.OwnerID == "" {
		return LockRequest{}, errors.New("lock owner is required")
	}
	if request.TTL <= 0 {
		return LockRequest{}, errors.New("lock TTL must be positive")
	}
	if len(request.Resources) == 0 {
		return LockRequest{}, errors.New("at least one lock resource is required")
	}
	seen := map[string]struct{}{}
	out := make([]LockResource, 0, len(request.Resources))
	for _, resource := range request.Resources {
		key := strings.TrimSpace(resource.Key)
		if key == "" {
			return LockRequest{}, errors.New("lock resource key is required")
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, LockResource{Key: key})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	request.Resources = out
	return request, nil
}

func ValidateLease(lease LockLease, now time.Time) error {
	if strings.TrimSpace(lease.ID) == "" || strings.TrimSpace(lease.OwnerID) == "" {
		return errors.New("lock lease identity is incomplete")
	}
	if lease.AcquiredAt.IsZero() || lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(lease.AcquiredAt) {
		return errors.New("lock lease times are invalid")
	}
	if !now.Before(lease.ExpiresAt) {
		return fmt.Errorf("lock lease %s is expired", lease.ID)
	}
	if len(lease.Resources) == 0 {
		return errors.New("lock lease has no resources")
	}
	return nil
}

func ValidateLeaseCoverage(lease LockLease, ownerID string, resources []LockResource, now time.Time) error {
	if err := ValidateLease(lease, now); err != nil {
		return err
	}
	if strings.TrimSpace(ownerID) == "" || lease.OwnerID != ownerID {
		return fmt.Errorf("lock lease %q is not owned by %q", lease.ID, ownerID)
	}
	request, err := NormalizeLockRequest(LockRequest{OwnerID: ownerID, Resources: resources, TTL: time.Second})
	if err != nil {
		return err
	}
	covered := make(map[string]struct{}, len(lease.Resources))
	for _, resource := range lease.Resources {
		covered[resource.Key] = struct{}{}
	}
	for _, resource := range request.Resources {
		if _, ok := covered[resource.Key]; !ok {
			return fmt.Errorf("lock lease %q does not cover resource %q", lease.ID, resource.Key)
		}
	}
	return nil
}
