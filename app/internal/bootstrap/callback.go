package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	CallbackModeExplicit       = "explicit"
	CallbackModeAutomaticRoute = "automatic_route"
	CallbackStateReady         = "ready"
	CallbackStateUnavailable   = "unavailable"
)

type CallbackStatus struct {
	Mode                  string `json:"mode"`
	State                 string `json:"state"`
	AgentListenAddress    string `json:"agent_listen_address"`
	EffectiveAdvertiseURL string `json:"effective_advertise_url,omitempty"`
	RuntimeProbeRequired  bool   `json:"runtime_probe_required"`
}

type callbackResolver interface {
	Resolve(context.Context, ProbeInput) (string, error)
	Status() CallbackStatus
}

type staticCallbackResolver struct {
	agentListenAddress string
	advertiseURL       string
}

func newStaticCallbackResolver(agentListenAddress, advertiseURL string) (*staticCallbackResolver, error) {
	advertiseURL = strings.TrimSpace(advertiseURL)
	if err := ValidateAgentAdvertiseURL(advertiseURL); err != nil {
		return nil, err
	}
	return &staticCallbackResolver{
		agentListenAddress: strings.TrimSpace(agentListenAddress),
		advertiseURL:       advertiseURL,
	}, nil
}

func (resolver *staticCallbackResolver) Resolve(context.Context, ProbeInput) (string, error) {
	if err := ValidateRemoteAdvertiseURL(resolver.advertiseURL); err != nil {
		return "", err
	}
	return resolver.advertiseURL, nil
}

func (resolver *staticCallbackResolver) Status() CallbackStatus {
	state := CallbackStateReady
	if err := ValidateRemoteAdvertiseURL(resolver.advertiseURL); err != nil {
		state = CallbackStateUnavailable
	}
	return CallbackStatus{
		Mode:                  CallbackModeExplicit,
		State:                 state,
		AgentListenAddress:    resolver.agentListenAddress,
		EffectiveAdvertiseURL: resolver.advertiseURL,
		RuntimeProbeRequired:  true,
	}
}

type routeSourceLookup func(context.Context, string, uint16) ([]net.IP, error)

type automaticCallbackResolver struct {
	agentListenAddress string
	listenHost         string
	listenPort         int
	lookup             routeSourceLookup
}

func newAutomaticCallbackResolver(agentListenAddress string, lookup routeSourceLookup) (*automaticCallbackResolver, error) {
	agentListenAddress = strings.TrimSpace(agentListenAddress)
	host, portText, err := net.SplitHostPort(agentListenAddress)
	if err != nil {
		return nil, fmt.Errorf("validate automatic Agent callback listener: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("automatic Agent callback listener requires a numeric TCP port")
	}
	if host != "" && net.ParseIP(host) == nil {
		return nil, errors.New("automatic Agent callback listener host must be a literal IP or wildcard")
	}
	if lookup == nil {
		lookup = lookupRouteSources
	}
	return &automaticCallbackResolver{
		agentListenAddress: agentListenAddress,
		listenHost:         host,
		listenPort:         port,
		lookup:             lookup,
	}, nil
}

func (resolver *automaticCallbackResolver) Resolve(ctx context.Context, input ProbeInput) (string, error) {
	address, port := input.Address, input.Port
	if input.Gateway != nil {
		address, port = input.Gateway.Address, input.Gateway.Port
	}
	sources, err := resolver.lookup(ctx, strings.TrimSpace(address), port)
	if err != nil {
		return "", &Error{
			Code:    ErrorAdvertiseURLUnavailable,
			Message: "Setpoint could not determine a routable Agent callback for this bootstrap target",
			Err:     err,
		}
	}
	candidates := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if !usableRemoteCallbackIP(source) || !resolver.listenerAccepts(source) {
			continue
		}
		candidate := (&url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(source.String(), strconv.Itoa(resolver.listenPort)),
		}).String()
		if err := ValidateRemoteAdvertiseURL(candidate); err != nil {
			continue
		}
		candidates[candidate] = struct{}{}
	}
	if len(candidates) == 0 {
		return "", &Error{
			Code:    ErrorAdvertiseURLUnavailable,
			Message: "no routable Agent callback matches the listener for this bootstrap target",
		}
	}
	if len(candidates) > 1 {
		return "", &Error{
			Code:    ErrorAdvertiseURLUnavailable,
			Message: "Agent callback route is ambiguous for this bootstrap target",
		}
	}
	for candidate := range candidates {
		return candidate, nil
	}
	panic("unreachable")
}

func (resolver *automaticCallbackResolver) Status() CallbackStatus {
	state := CallbackStateReady
	if !resolver.listenerCanServeRemoteCallback() {
		state = CallbackStateUnavailable
	}
	return CallbackStatus{
		Mode:                 CallbackModeAutomaticRoute,
		State:                state,
		AgentListenAddress:   resolver.agentListenAddress,
		RuntimeProbeRequired: true,
	}
}

func (resolver *automaticCallbackResolver) listenerCanServeRemoteCallback() bool {
	if resolver.listenHost == "" {
		return true
	}
	ip := net.ParseIP(resolver.listenHost)
	return ip != nil && !ip.IsLoopback() && (ip.IsUnspecified() || ip.IsGlobalUnicast())
}

func (resolver *automaticCallbackResolver) listenerAccepts(source net.IP) bool {
	if resolver.listenHost == "" {
		return true
	}
	listenIP := net.ParseIP(resolver.listenHost)
	if listenIP == nil || listenIP.IsLoopback() {
		return false
	}
	if listenIP.IsUnspecified() {
		if listenIP.To4() != nil {
			return source.To4() != nil
		}
		return source.To4() == nil
	}
	return listenIP.Equal(source)
}

func usableRemoteCallbackIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() && ip.IsGlobalUnicast()
}

func lookupRouteSources(ctx context.Context, address string, port uint16) ([]net.IP, error) {
	if address == "" {
		return nil, errors.New("route address is required")
	}
	var destinations []net.IP
	if literal := net.ParseIP(address); literal != nil {
		destinations = []net.IP{literal}
	} else {
		resolved, err := net.DefaultResolver.LookupIPAddr(ctx, address)
		if err != nil {
			return nil, fmt.Errorf("resolve route destination %q: %w", address, err)
		}
		for _, item := range resolved {
			destinations = append(destinations, item.IP)
		}
	}
	if len(destinations) == 0 {
		return nil, fmt.Errorf("route destination %q resolved to no addresses", address)
	}

	seen := map[string]struct{}{}
	sources := make([]net.IP, 0, len(destinations))
	var routeErr error
	for _, destination := range destinations {
		if destination == nil {
			continue
		}
		network := "udp6"
		if destination.To4() != nil {
			network = "udp4"
		}
		connection, err := (&net.Dialer{}).DialContext(
			ctx,
			network,
			net.JoinHostPort(destination.String(), strconv.Itoa(int(port))),
		)
		if err != nil {
			routeErr = errors.Join(routeErr, err)
			continue
		}
		local, ok := connection.LocalAddr().(*net.UDPAddr)
		_ = connection.Close()
		if !ok || local.IP == nil {
			routeErr = errors.Join(routeErr, errors.New("route probe returned an unexpected local address"))
			continue
		}
		key := local.IP.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		sources = append(sources, append(net.IP(nil), local.IP...))
	}
	if len(sources) == 0 {
		if routeErr == nil {
			routeErr = errors.New("no routable source address was found")
		}
		return nil, routeErr
	}
	return sources, nil
}
