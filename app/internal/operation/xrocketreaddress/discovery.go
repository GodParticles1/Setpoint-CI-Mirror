package xrocketreaddress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"

	"setpoint/internal/executor"
)

const discoverySchema = "xrocket.readdress.discovery.v1"

var generationPattern = regexp.MustCompile(`V[0-9]{3}R[0-9]{3}C[0-9]{2}(?:_Base)?B[0-9]{3}`)

type discoveryProbe struct {
	executor executor.CommandExecutor
}

type ipAddressView struct {
	IfName   string `json:"ifname"`
	AddrInfo []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
		Scope     string `json:"scope"`
	} `json:"addr_info"`
}

type routeView struct {
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
	PrefSrc string `json:"prefsrc"`
	Metric  int    `json:"metric"`
}

type networkObservation struct {
	addresses []ipAddressView
	routes    []routeView
}

func (probe discoveryProbe) discover(ctx context.Context, nodeID string) (discoveryState, error) {
	state := discoveryState{SchemaVersion: discoverySchema, NodeID: nodeID}
	networkState, err := probe.observeNetwork(ctx)
	if err != nil {
		return discoveryState{}, err
	}
	configPath, config, found, err := probe.readKeepalivedConfig(ctx)
	if err != nil {
		return discoveryState{}, err
	}
	if !found {
		state.Unresolved = append(state.Unresolved, "keepalived_config")
	} else {
		parsed, parseErr := parseKeepalivedConfig(config)
		if parseErr != nil {
			state.Unresolved = append(state.Unresolved, "keepalived_topology:"+parseErr.Error())
		} else {
			state.KeepalivedConfigPath = configPath
			state.ConfiguredRole = parsed.State
			state.Interface = parsed.Interface
			state.VIPAddress = parsed.VIPAddress
			switch parsed.State {
			case "MASTER":
				state.MasterAddress, state.SlaveAddress = parsed.SourceAddress, parsed.PeerAddress
			case "BACKUP":
				state.MasterAddress, state.SlaveAddress = parsed.PeerAddress, parsed.SourceAddress
			default:
				state.Unresolved = append(state.Unresolved, "configured_master_slave_role")
			}
			state.RuntimeRole = "standby"
			if networkState.containsAddress(parsed.VIPAddress) {
				state.RuntimeRole = "active_vip_owner"
			}
		}
	}
	expectedLocal := ""
	switch state.ConfiguredRole {
	case "MASTER":
		expectedLocal = state.MasterAddress
	case "BACKUP":
		expectedLocal = state.SlaveAddress
	}
	localAddress, prefix, gateway, device, networkErr := networkState.primaryIPv4(expectedLocal)
	if networkErr != nil {
		state.Unresolved = append(state.Unresolved, "network:"+networkErr.Error())
	} else {
		state.PrefixLength = prefix
		state.GatewayAddress = gateway
		if state.Interface == "" {
			state.Interface = device
		}
		if localAddress != state.MasterAddress && localAddress != state.SlaveAddress {
			state.Unresolved = append(state.Unresolved, "local_address_topology_correlation")
		}
	}
	productPath, productFound, productErr := probe.resolveProductPath(ctx)
	if productErr != nil {
		return discoveryState{}, productErr
	}
	if productFound {
		state.ProductVersionEvidence = productPath
		state.ProductGeneration = generationPattern.FindString(productPath)
	}
	if state.ProductGeneration == "" {
		state.Unresolved = append(state.Unresolved, "xrocket_generation_version")
	}
	state.Unresolved = uniqueSorted(state.Unresolved)
	return state, nil
}

func (probe discoveryProbe) observeNetwork(ctx context.Context) (networkObservation, error) {
	addressesResult, err := probe.execute(ctx, executor.Command{Name: "ip", Args: []string{"-j", "address", "show"}})
	if err != nil {
		return networkObservation{}, fmt.Errorf("discover IPv4 addresses: %w", err)
	}
	routesResult, err := probe.execute(ctx, executor.Command{Name: "ip", Args: []string{"-j", "route", "show", "default"}})
	if err != nil {
		return networkObservation{}, fmt.Errorf("discover default route: %w", err)
	}
	var observation networkObservation
	if err := json.Unmarshal([]byte(addressesResult), &observation.addresses); err != nil {
		return networkObservation{}, fmt.Errorf("decode ip address JSON: %w", err)
	}
	if err := json.Unmarshal([]byte(routesResult), &observation.routes); err != nil {
		return networkObservation{}, fmt.Errorf("decode default route JSON: %w", err)
	}
	return observation, nil
}

func (probe discoveryProbe) readKeepalivedConfig(ctx context.Context) (string, string, bool, error) {
	rootPath := "/etc/keepalived/keepalived.conf"
	candidates := []string{rootPath}
	home, err := probe.execute(ctx, executor.Command{Name: "printenv", Args: []string{"HOME"}})
	if err == nil {
		home = strings.TrimSpace(home)
		if strings.HasPrefix(home, "/") && path.Clean(home) == home {
			homePath := path.Join(home, "etc/keepalived/keepalived.conf")
			if home != "/root" {
				candidates = []string{homePath, rootPath}
			} else {
				candidates = append(candidates, homePath)
			}
		}
	}
	for _, candidate := range orderedUnique(candidates) {
		exists, existsErr := probe.fileExists(ctx, candidate)
		if existsErr != nil {
			return "", "", false, existsErr
		}
		if !exists {
			continue
		}
		content, readErr := probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", candidate}})
		if readErr != nil {
			return "", "", false, fmt.Errorf("read keepalived config %s: %w", candidate, readErr)
		}
		return candidate, content, true, nil
	}
	return "", "", false, nil
}

func (probe discoveryProbe) resolveProductPath(ctx context.Context) (string, bool, error) {
	home, _ := probe.execute(ctx, executor.Command{Name: "printenv", Args: []string{"HOME"}})
	rootPath := "/opt/data/xrocket"
	candidates := []string{rootPath}
	home = strings.TrimSpace(home)
	if strings.HasPrefix(home, "/") && path.Clean(home) == home {
		homePath := path.Join(home, "opt/data/xrocket")
		if home != "/root" {
			candidates = []string{homePath, rootPath}
		} else {
			candidates = append(candidates, homePath)
		}
	}
	for _, candidate := range orderedUnique(candidates) {
		exists, err := probe.fileExists(ctx, candidate)
		if err != nil {
			return "", false, err
		}
		if !exists {
			continue
		}
		resolved, err := probe.execute(ctx, executor.Command{Name: "readlink", Args: []string{"-f", "--", candidate}})
		if err != nil {
			return "", false, fmt.Errorf("resolve xRocket product path %s: %w", candidate, err)
		}
		return strings.TrimSpace(resolved), true, nil
	}
	return "", false, nil
}

func (probe discoveryProbe) fileExists(ctx context.Context, filePath string) (bool, error) {
	_, err := probe.executor.Execute(ctx, executor.Command{Name: "test", Args: []string{"-e", filePath}})
	if err == nil {
		return true, nil
	}
	var commandErr *executor.Error
	if errors.As(err, &commandErr) && commandErr.Kind == executor.ErrorExit && commandErr.Result.ExitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect path %s: %w", filePath, err)
}

func (probe discoveryProbe) execute(ctx context.Context, command executor.Command) (string, error) {
	result, err := probe.executor.Execute(ctx, command)
	if err != nil {
		return "", err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", fmt.Errorf("command %s output was truncated", command.Name)
	}
	return result.Stdout, nil
}

func (observation networkObservation) containsAddress(address string) bool {
	parsed := net.ParseIP(address)
	if parsed == nil {
		return false
	}
	for _, device := range observation.addresses {
		for _, current := range device.AddrInfo {
			if net.ParseIP(current.Local).Equal(parsed) {
				return true
			}
		}
	}
	return false
}

func (observation networkObservation) primaryIPv4(preferredAddress string) (string, int, string, string, error) {
	if len(observation.routes) == 0 {
		return "", 0, "", "", errors.New("default route is missing")
	}
	routes := append([]routeView(nil), observation.routes...)
	sort.SliceStable(routes, func(left, right int) bool { return routes[left].Metric < routes[right].Metric })
	for _, route := range routes {
		gateway := net.ParseIP(route.Gateway)
		if gateway == nil || gateway.To4() == nil || route.Dev == "" {
			continue
		}
		var candidates []struct {
			address string
			prefix  int
		}
		for _, device := range observation.addresses {
			if device.IfName != route.Dev {
				continue
			}
			for _, address := range device.AddrInfo {
				ip := net.ParseIP(address.Local)
				if address.Family != "inet" || address.Scope != "global" || ip == nil || ip.To4() == nil {
					continue
				}
				candidates = append(candidates, struct {
					address string
					prefix  int
				}{address: ip.String(), prefix: address.PrefixLen})
			}
		}
		wanted := route.PrefSrc
		if wanted == "" {
			wanted = preferredAddress
		}
		if wanted != "" {
			for _, candidate := range candidates {
				if candidate.address == wanted {
					return candidate.address, candidate.prefix, gateway.To4().String(), route.Dev, nil
				}
			}
			continue
		}
		if len(candidates) == 1 {
			return candidates[0].address, candidates[0].prefix, gateway.To4().String(), route.Dev, nil
		}
	}
	return "", 0, "", "", errors.New("default route cannot be correlated with one global IPv4 address")
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func orderedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
