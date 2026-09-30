// Package remediationbindings owns the bounded Server compile-time composition.
package remediationbindings

import (
	"setpoint/internal/operation/sysctlrepair"
	"setpoint/internal/remediation"
)

// New constructs the same reviewed registry for API offers and confirmation.
// Future Operations add their package-owned binding here, never in checkrun.
func New() (*remediation.Registry, error) {
	return remediation.NewRegistry(sysctlrepair.RemediationBinding())
}
