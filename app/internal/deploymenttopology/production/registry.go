package production

import (
	"setpoint/internal/deploymenttopology"
	"setpoint/internal/deploymenttopology/providers/xrocketnodes"
)

// Providers returns the bounded local collectors wired into the production
// Agent. Ordering is explicit and stable; adding a provider is an architecture
// decision because conflicting positive claims must remain visible to the
// generic resolver.
func Providers() []deploymenttopology.Provider {
	return []deploymenttopology.Provider{
		xrocketnodes.New(),
	}
}

func NewRegistry() (*deploymenttopology.ProviderRegistry, error) {
	return deploymenttopology.NewProviderRegistry(Providers()...)
}
