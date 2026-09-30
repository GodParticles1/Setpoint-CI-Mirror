// Package remediation defines Server-owned, compile-time finding bindings.
// Check results and HTTP clients cannot register or override these declarations.
package remediation

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

// Impact declares the reviewed upper bound. A finding outside it is rejected.
// AUTO_SAFE bindings cannot accept connection, business or restart impact.
type Impact struct {
	Connection bool
	Business   bool
	Restart    bool
}

type Binding struct {
	CheckIDs         []string
	OperationID      string
	Disposition      plugin.RemediationDisposition
	Impact           Impact
	SupportsRollback bool
	// Parameters must validate the exact finding shape before constructing fixed
	// operation parameters. No parameters or authority come from the client.
	Parameters func(task.CheckItem) (map[string]string, error)
}

// Registry is immutable after construction; duplicate ownership fails closed.
type Registry struct{ byCheck map[string]Binding }

func NewRegistry(bindings ...Binding) (*Registry, error) {
	registry := &Registry{byCheck: make(map[string]Binding)}
	for _, binding := range bindings {
		if binding.Disposition != plugin.RemediationAutoSafe && binding.Disposition != plugin.RemediationControlled {
			return nil, errors.New("binding requires AUTO_SAFE or CONTROLLED disposition")
		}
		if binding.OperationID == "" || binding.OperationID != strings.TrimSpace(binding.OperationID) || plugin.ValidateRemediationMetadata(plugin.RemediationMetadata{Disposition: binding.Disposition, OperationID: binding.OperationID, Reason: "binding"}) != nil {
			return nil, errors.New("binding requires an exact valid operation ID")
		}
		if len(binding.CheckIDs) == 0 || binding.Parameters == nil || !binding.SupportsRollback {
			return nil, errors.New("binding requires checks, finding validation/parameters and rollback")
		}
		if binding.Disposition == plugin.RemediationAutoSafe && binding.Impact != (Impact{}) {
			return nil, errors.New("AUTO_SAFE binding cannot accept connection, business or restart impact")
		}
		for _, id := range binding.CheckIDs {
			if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
				return nil, errors.New("binding requires exact nonempty check IDs")
			}
			if _, exists := registry.byCheck[id]; exists {
				return nil, fmt.Errorf("duplicate binding for check %q", id)
			}
			entry := binding
			entry.CheckIDs = nil // caller-owned slices are never retained
			registry.byCheck[id] = entry
		}
	}
	return registry, nil
}

type Capability struct {
	OperationID          string
	Parameters           map[string]string
	SupportsAutomaticFix bool
	SupportsRollback     bool
}

func (registry *Registry) Resolve(item task.CheckItem, metadata plugin.RemediationMetadata) (Capability, error) {
	if registry == nil {
		return Capability{}, errors.New("remediation binding registry is unavailable")
	}
	binding, ok := registry.byCheck[item.ID]
	if !ok || plugin.ValidateRemediationMetadata(metadata) != nil || binding.OperationID != metadata.OperationID || binding.Disposition != metadata.Disposition {
		return Capability{}, errors.New("no reviewed binding matches check, operation and disposition")
	}
	if item.Status != task.ItemUnsafe || strings.TrimSpace(item.CurrentValue) == "" || strings.TrimSpace(item.RecommendedValue) == "" {
		return Capability{}, errors.New("binding requires an unsafe finding with current and recommended values")
	}
	if (item.MayAffectConnection && !binding.Impact.Connection) || (item.MayAffectBusiness && !binding.Impact.Business) || (item.RequiresRestart && !binding.Impact.Restart) {
		return Capability{}, errors.New("finding impact is outside the reviewed binding envelope")
	}
	parameters, err := binding.Parameters(item)
	if err != nil {
		return Capability{}, fmt.Errorf("finding does not match binding: %w", err)
	}
	if len(parameters) == 0 {
		return Capability{}, errors.New("binding produced no parameters")
	}
	return Capability{OperationID: binding.OperationID, Parameters: maps.Clone(parameters), SupportsAutomaticFix: binding.Disposition == plugin.RemediationAutoSafe, SupportsRollback: binding.SupportsRollback}, nil
}
