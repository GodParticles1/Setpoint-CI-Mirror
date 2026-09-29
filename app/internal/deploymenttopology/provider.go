package deploymenttopology

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Provider interface {
	ID() string
	Collect(context.Context) ([]Evidence, error)
}

type ProviderRegistry struct {
	providers []Provider
}

func NewProviderRegistry(providers ...Provider) (*ProviderRegistry, error) {
	registry := &ProviderRegistry{}
	seen := map[string]struct{}{}
	for _, provider := range providers {
		if provider == nil {
			return nil, errors.New("topology provider is required")
		}
		id := strings.TrimSpace(provider.ID())
		if id == "" {
			return nil, errors.New("topology provider id is required")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate topology provider id %q", id)
		}
		seen[id] = struct{}{}
		registry.providers = append(registry.providers, provider)
	}
	return registry, nil
}

func (registry *ProviderRegistry) Discover(ctx context.Context) (Result, error) {
	if registry == nil {
		return Result{}, errors.New("topology provider registry is required")
	}
	evidence := make([]Evidence, 0)
	for _, provider := range registry.providers {
		observed, err := provider.Collect(ctx)
		if err != nil {
			return Result{}, fmt.Errorf("topology provider %q: %w", provider.ID(), err)
		}
		evidence = append(evidence, observed...)
	}
	resolved, err := Resolve(evidence)
	if err != nil {
		return Result{}, err
	}
	return WithParticipantTrust(resolved), nil
}
