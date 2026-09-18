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
	"strconv"
	"strings"

	"setpoint/internal/executor"
)

const (
	installProfileRootNative = "ROOT_NATIVE"
	installProfileHomePrefix = "HOME_PREFIXED_NON_ROOT_PRODUCT"
)

type runtimeProfile struct {
	InstallProfile        string   `json:"install_profile"`
	OSAuthorityUID        int      `json:"os_authority_uid"`
	ProductUser           string   `json:"product_user"`
	ProductHome           string   `json:"product_home"`
	ProductPrefix         string   `json:"product_prefix"`
	XrocketBinary         string   `json:"xrocket_binary"`
	XrocketVersion        string   `json:"xrocket_version"`
	CommonYAMLPath        string   `json:"common_yaml_path"`
	ExternalDBAddress     string   `json:"external_db_address"`
	ExternalDBPort        int      `json:"external_db_port"`
	NetworkBackend        string   `json:"network_backend"`
	NetworkConfigPath     string   `json:"network_config_path"`
	PersistentAddress     string   `json:"persistent_address"`
	PersistentPrefix      int      `json:"persistent_prefix"`
	PersistentGateway     string   `json:"persistent_gateway"`
	EtcdConfigPath        string   `json:"etcd_config_path"`
	EtcdctlPath           string   `json:"etcdctl_path"`
	EtcdScheme            string   `json:"etcd_scheme"`
	EtcdClientPort        int      `json:"etcd_client_port"`
	EtcdPeerPort          int      `json:"etcd_peer_port"`
	EtcdMemberID          string   `json:"etcd_member_id"`
	EtcdMemberCount       int      `json:"etcd_member_count"`
	EtcdServiceName       string   `json:"etcd_service_name"`
	ServiceControlAdapter string   `json:"service_control_adapter"`
	BootID                string   `json:"boot_id"`
	ConfdDestinations     []string `json:"confd_destinations,omitempty"`
}

type goldendbObservation struct {
	Address string
	Port    int
}

type etcdObservation struct {
	Scheme     string
	ClientPort int
	PeerPort   int
}

func (probe discoveryProbe) discoverRuntimeProfile(ctx context.Context, state discoveryState) (runtimeProfile, error) {
	profile := runtimeProfile{NetworkBackend: "ifcfg", ServiceControlAdapter: "monit"}
	uidText, err := probe.execute(ctx, executor.Command{Name: "id", Args: []string{"-u"}})
	if err != nil {
		return profile, fmt.Errorf("prove OS authority: %w", err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(uidText))
	if err != nil {
		return profile, fmt.Errorf("parse OS authority uid: %w", err)
	}
	profile.OSAuthorityUID = uid
	if uid != 0 {
		return profile, errors.New("xRocket readdress requires root OS authority on the Agent")
	}

	owner, binary, err := probe.discoverActiveXrocket(ctx)
	if err != nil {
		return profile, err
	}
	profile.ProductUser = owner
	profile.XrocketBinary = binary
	const suffix = "/opt/data/xrocket/xrocket.v2"
	if !strings.HasSuffix(binary, suffix) {
		return profile, fmt.Errorf("active xrocket path %q is outside the bounded native layout", binary)
	}
	prefix := strings.TrimSuffix(binary, suffix)
	profile.ProductPrefix = prefix
	if prefix == "" {
		if owner != "root" {
			return profile, errors.New("root-native xRocket binary is not owned by the active root product identity")
		}
		profile.InstallProfile = installProfileRootNative
		profile.ProductHome = "/root"
	} else {
		if owner == "root" || !strings.HasPrefix(prefix, "/") || path.Clean(prefix) != prefix {
			return profile, errors.New("HOME-prefixed xRocket product identity is invalid")
		}
		home, err := probe.lookupUserHome(ctx, owner)
		if err != nil {
			return profile, err
		}
		if home != prefix {
			return profile, fmt.Errorf("product prefix %q does not equal discovered HOME %q", prefix, home)
		}
		profile.InstallProfile = installProfileHomePrefix
		profile.ProductHome = home
	}

	version, err := probe.productExecute(ctx, profile, []string{"--version"})
	if err != nil {
		return profile, fmt.Errorf("read xrocket version: %w", err)
	}
	profile.XrocketVersion = strings.TrimSpace(version)
	if profile.XrocketVersion == "" {
		return profile, errors.New("xrocket version output is empty")
	}

	profile.CommonYAMLPath, err = probe.findUniqueFile(ctx, prefixed(profile.ProductPrefix, "/etc"), "common.yaml", "/confd/")
	if err != nil {
		return profile, fmt.Errorf("discover common.yaml: %w", err)
	}
	common, err := probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", profile.CommonYAMLPath}})
	if err != nil {
		return profile, fmt.Errorf("read common.yaml: %w", err)
	}
	db, err := parseGoldendb(common)
	if err != nil {
		return profile, fmt.Errorf("discover external DB target: %w", err)
	}
	profile.ExternalDBAddress, profile.ExternalDBPort = db.Address, db.Port

	profile.NetworkConfigPath = "/etc/sysconfig/network-scripts/ifcfg-" + state.Interface
	if exists, err := probe.fileExists(ctx, profile.NetworkConfigPath); err != nil {
		return profile, err
	} else if !exists {
		return profile, fmt.Errorf("ifcfg network backend is required: %s is missing", profile.NetworkConfigPath)
	}
	ifcfg, err := probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", profile.NetworkConfigPath}})
	if err != nil {
		return profile, fmt.Errorf("read persistent network config: %w", err)
	}
	profile.PersistentAddress, profile.PersistentPrefix, profile.PersistentGateway, err = parseIfcfg(ifcfg)
	if err != nil {
		return profile, err
	}
	if profile.PersistentAddress != state.MasterAddress || profile.PersistentPrefix != state.PrefixLength || profile.PersistentGateway != state.GatewayAddress {
		return profile, errors.New("persistent ifcfg network truth differs from current runtime topology")
	}

	profile.EtcdConfigPath, err = probe.findUniqueFile(ctx, prefixed(profile.ProductPrefix, "/etc"), "etcd.conf", "/etcd/")
	if err != nil {
		return profile, fmt.Errorf("discover etcd config: %w", err)
	}
	etcdConfig, err := probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", profile.EtcdConfigPath}})
	if err != nil {
		return profile, fmt.Errorf("read etcd config: %w", err)
	}
	etcd, err := parseEtcdConfig(etcdConfig, state.MasterAddress)
	if err != nil {
		return profile, err
	}
	profile.EtcdScheme, profile.EtcdClientPort, profile.EtcdPeerPort = etcd.Scheme, etcd.ClientPort, etcd.PeerPort
	profile.EtcdctlPath, err = probe.findUniqueExecutable(ctx, prefixed(profile.ProductPrefix, "/opt"), "etcdctl")
	if err != nil {
		return profile, fmt.Errorf("discover etcdctl: %w", err)
	}
	endpoint := fmt.Sprintf("%s://%s:%d", profile.EtcdScheme, state.MasterAddress, profile.EtcdClientPort)
	if _, err := probe.execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", profile.EtcdctlPath, "--endpoints=" + endpoint, "endpoint", "health"}}); err != nil {
		return profile, fmt.Errorf("etcd endpoint health: %w", err)
	}
	memberJSON, err := probe.execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", profile.EtcdctlPath, "--endpoints=" + endpoint, "member", "list", "-w", "json"}})
	if err != nil {
		return profile, fmt.Errorf("etcd member list: %w", err)
	}
	profile.EtcdMemberID, profile.EtcdMemberCount, err = parseEtcdMemberList(memberJSON, state.MasterAddress, profile.EtcdPeerPort)
	if err != nil {
		return profile, err
	}

	monitSummary, err := probe.execute(ctx, executor.Command{Name: "monit", Args: []string{"summary"}})
	if err != nil {
		return profile, fmt.Errorf("read Monit service state: %w", err)
	}
	profile.EtcdServiceName, err = parseEtcdMonitService(monitSummary)
	if err != nil {
		return profile, err
	}
	profile.BootID, err = probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", "/proc/sys/kernel/random/boot_id"}})
	if err != nil {
		return profile, fmt.Errorf("read boot_id: %w", err)
	}
	profile.BootID = strings.TrimSpace(profile.BootID)
	if profile.BootID == "" {
		return profile, errors.New("boot_id is empty")
	}
	profile.ConfdDestinations, err = probe.discoverConfdDestinations(ctx, profile.ProductPrefix)
	if err != nil {
		return profile, err
	}
	return profile, nil
}

func (probe discoveryProbe) discoverActiveXrocket(ctx context.Context) (string, string, error) {
	output, err := probe.execute(ctx, executor.Command{Name: "ps", Args: []string{"-eo", "user=,args="}})
	if err != nil {
		return "", "", fmt.Errorf("discover active xrocket process: %w", err)
	}
	type candidate struct{ owner, path string }
	seen := map[string]candidate{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		owner := fields[0]
		for _, field := range fields[1:] {
			if strings.HasSuffix(field, "/opt/data/xrocket/xrocket.v2") && strings.HasPrefix(field, "/") && path.Clean(field) == field {
				seen[owner+"\x00"+field] = candidate{owner: owner, path: field}
			}
		}
	}
	if len(seen) != 1 {
		return "", "", fmt.Errorf("expected one active xrocket.v2 identity, found %d", len(seen))
	}
	for _, value := range seen {
		return value.owner, value.path, nil
	}
	return "", "", errors.New("active xrocket.v2 identity is missing")
}

func (probe discoveryProbe) lookupUserHome(ctx context.Context, user string) (string, error) {
	output, err := probe.execute(ctx, executor.Command{Name: "getent", Args: []string{"passwd", user}})
	if err != nil {
		return "", fmt.Errorf("resolve product user %s: %w", user, err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		return "", errors.New("product user lookup is ambiguous")
	}
	fields := strings.Split(lines[0], ":")
	if len(fields) < 7 || fields[0] != user || !strings.HasPrefix(fields[5], "/") || path.Clean(fields[5]) != fields[5] {
		return "", errors.New("product user HOME is invalid")
	}
	return fields[5], nil
}

func (probe discoveryProbe) productExecute(ctx context.Context, profile runtimeProfile, args []string) (string, error) {
	if profile.InstallProfile == installProfileRootNative {
		return probe.execute(ctx, executor.Command{Name: profile.XrocketBinary, Args: append([]string(nil), args...)})
	}
	if profile.InstallProfile != installProfileHomePrefix || profile.ProductUser == "" || profile.ProductHome == "" {
		return "", errors.New("product execution identity is incomplete")
	}
	commandArgs := []string{"-u", profile.ProductUser, "--", "env", "HOME=" + profile.ProductHome, profile.XrocketBinary}
	commandArgs = append(commandArgs, args...)
	return probe.execute(ctx, executor.Command{Name: "runuser", Args: commandArgs})
}

func prefixed(prefix, absolute string) string {
	if prefix == "" {
		return absolute
	}
	return path.Join(prefix, absolute)
}

func (probe discoveryProbe) findUniqueFile(ctx context.Context, root, name, pathMarker string) (string, error) {
	output, err := probe.execute(ctx, executor.Command{Name: "find", Args: []string{root, "-type", "f", "-name", name, "-print"}})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, value := range strings.Split(output, "\n") {
		value = strings.TrimSpace(value)
		if value != "" && strings.Contains(value, pathMarker) && strings.HasPrefix(value, "/") && path.Clean(value) == value {
			matches = append(matches, value)
		}
	}
	matches = orderedUnique(matches)
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one %s under %s, found %d", name, root, len(matches))
	}
	return matches[0], nil
}

func (probe discoveryProbe) findUniqueExecutable(ctx context.Context, root, name string) (string, error) {
	output, err := probe.execute(ctx, executor.Command{Name: "find", Args: []string{root, "-type", "f", "-name", name, "-perm", "-111", "-print"}})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, value := range strings.Split(output, "\n") {
		value = strings.TrimSpace(value)
		if value != "" && strings.HasPrefix(value, "/") && path.Clean(value) == value {
			matches = append(matches, value)
		}
	}
	matches = orderedUnique(matches)
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one executable %s under %s, found %d", name, root, len(matches))
	}
	return matches[0], nil
}

func parseGoldendb(content string) (goldendbObservation, error) {
	block, err := topLevelYAMLBlock(content, "goldendb")
	if err != nil {
		return goldendbObservation{}, err
	}
	fieldPattern := regexp.MustCompile(`(?i)^\s*(host|hostname|address|ip|endpoint)\s*:\s*(.*?)\s*$`)
	ipPattern := regexp.MustCompile(`(?:^|[^0-9])((?:[0-9]{1,3}\.){3}[0-9]{1,3})(?:[^0-9]|$)`)
	portPattern := regexp.MustCompile(`(?i)^\s*port\s*:\s*["']?([0-9]+)`)
	addresses := map[string]struct{}{}
	ports := map[int]struct{}{}
	for _, line := range strings.Split(block, "\n") {
		if match := fieldPattern.FindStringSubmatch(line); len(match) == 3 {
			value := strings.SplitN(match[2], "#", 2)[0]
			for _, candidate := range ipPattern.FindAllStringSubmatch(value, -1) {
				if len(candidate) != 2 {
					continue
				}
				if ip := net.ParseIP(candidate[1]); ip != nil && ip.To4() != nil {
					addresses[ip.To4().String()] = struct{}{}
				}
			}
		}
		if match := portPattern.FindStringSubmatch(line); len(match) == 2 {
			port, _ := strconv.Atoi(match[1])
			if port >= 1 && port <= 65535 {
				ports[port] = struct{}{}
			}
		}
	}
	if len(addresses) != 1 || len(ports) != 1 {
		return goldendbObservation{}, fmt.Errorf("goldendb requires exactly one authoritative IPv4 endpoint and one access port, found %d/%d", len(addresses), len(ports))
	}
	result := goldendbObservation{}
	for address := range addresses {
		result.Address = address
	}
	for port := range ports {
		result.Port = port
	}
	return result, nil
}

func topLevelYAMLBlock(content, key string) (string, error) {
	lines := strings.Split(content, "\n")
	start := -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line)-len(strings.TrimLeft(line, " \t")) == 0 && strings.HasPrefix(trimmed, key+":") {
			if start != -1 {
				return "", fmt.Errorf("multiple top-level %s blocks", key)
			}
			start = index
		}
	}
	if start == -1 {
		return "", fmt.Errorf("top-level %s block is missing", key)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(line)-len(strings.TrimLeft(line, " \t")) == 0 {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), nil
}

func parseIfcfg(content string) (string, int, string, error) {
	values := parseKeyValueLines(content)
	ipaddrKey := regexp.MustCompile(`^IPADDR(?:[0-9]+)?$`)
	var addresses []string
	for key, value := range values {
		if ipaddrKey.MatchString(key) {
			if ip := net.ParseIP(value); ip != nil && ip.To4() != nil {
				addresses = append(addresses, ip.To4().String())
			}
		}
	}
	addresses = uniqueSorted(addresses)
	if len(addresses) != 1 {
		return "", 0, "", fmt.Errorf("ifcfg requires exactly one IPv4 address from IPADDR or IPADDR<n>, found %d", len(addresses))
	}
	prefix := 0
	if value := values["PREFIX"]; value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 32 {
			return "", 0, "", errors.New("ifcfg PREFIX is invalid")
		}
		prefix = parsed
	} else if value := values["NETMASK"]; value != "" {
		mask := net.ParseIP(value)
		if mask == nil || mask.To4() == nil {
			return "", 0, "", errors.New("ifcfg NETMASK is invalid")
		}
		ones, bits := net.IPMask(mask.To4()).Size()
		if bits != 32 || ones < 1 {
			return "", 0, "", errors.New("ifcfg NETMASK is non-contiguous")
		}
		prefix = ones
	} else {
		return "", 0, "", errors.New("ifcfg PREFIX/NETMASK is missing")
	}
	gateway := canonicalIPv4(values["GATEWAY"])
	if gateway == "" {
		return "", 0, "", errors.New("ifcfg GATEWAY is missing or invalid")
	}
	return addresses[0], prefix, gateway, nil
}

func parseKeyValueLines(content string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values
}

func parseEtcdConfig(content, currentAddress string) (etcdObservation, error) {
	values := parseKeyValueLines(content)
	clientURLs := values["ETCD_ADVERTISE_CLIENT_URLS"]
	peerURLs := values["ETCD_INITIAL_ADVERTISE_PEER_URLS"]
	clientScheme, clientAddress, clientPort, err := oneHTTPURL(clientURLs)
	if err != nil {
		return etcdObservation{}, fmt.Errorf("etcd client URL: %w", err)
	}
	_, peerAddress, peerPort, err := oneHTTPURL(peerURLs)
	if err != nil {
		return etcdObservation{}, fmt.Errorf("etcd peer URL: %w", err)
	}
	if clientAddress != currentAddress || peerAddress != currentAddress {
		return etcdObservation{}, errors.New("etcd advertised client/peer addresses do not match the current node address")
	}
	return etcdObservation{Scheme: clientScheme, ClientPort: clientPort, PeerPort: peerPort}, nil
}

func oneHTTPURL(value string) (string, string, int, error) {
	matcher := regexp.MustCompile(`^(https?)://((?:[0-9]{1,3}\.){3}[0-9]{1,3}):([0-9]+)$`)
	match := matcher.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) != 4 {
		return "", "", 0, errors.New("expected one explicit IPv4 URL")
	}
	address := canonicalIPv4(match[2])
	port, err := strconv.Atoi(match[3])
	if address == "" || err != nil || port < 1 || port > 65535 {
		return "", "", 0, errors.New("invalid advertised URL")
	}
	return match[1], address, port, nil
}

func parseEtcdMemberList(content, currentAddress string, peerPort int) (string, int, error) {
	var response struct {
		Members []struct {
			ID       uint64   `json:"ID"`
			PeerURLs []string `json:"peerURLs"`
		} `json:"members"`
	}
	if err := json.Unmarshal([]byte(content), &response); err != nil {
		return "", 0, fmt.Errorf("decode etcd member list: %w", err)
	}
	if len(response.Members) != 1 {
		return "", len(response.Members), fmt.Errorf("independent singleton etcd requires one member, found %d", len(response.Members))
	}
	member := response.Members[0]
	if member.ID == 0 || len(member.PeerURLs) != 1 {
		return "", 1, errors.New("singleton etcd member identity/peer URL is incomplete")
	}
	_, address, port, err := oneHTTPURL(member.PeerURLs[0])
	if err != nil || address != currentAddress || port != peerPort {
		return "", 1, errors.New("singleton etcd member peer URL differs from the discovered local topology")
	}
	return fmt.Sprintf("%x", member.ID), 1, nil
}

func parseEtcdMonitService(content string) (string, error) {
	matcher := regexp.MustCompile(`(?im)^\s*['"]?([A-Za-z0-9_.-]*etcd[A-Za-z0-9_.-]*)['"]?\s+`)
	seen := map[string]struct{}{}
	for _, match := range matcher.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			seen[match[1]] = struct{}{}
		}
	}
	if len(seen) != 1 {
		return "", fmt.Errorf("expected one Monit etcd service, found %d", len(seen))
	}
	for name := range seen {
		return name, nil
	}
	return "", errors.New("Monit etcd service is missing")
}

func (probe discoveryProbe) discoverConfdDestinations(ctx context.Context, prefix string) ([]string, error) {
	root := prefixed(prefix, "/etc/confd/conf.d")
	if exists, err := probe.fileExists(ctx, root); err != nil {
		return nil, err
	} else if !exists {
		return nil, fmt.Errorf("confd metadata directory is missing: %s", root)
	}
	output, err := probe.execute(ctx, executor.Command{Name: "find", Args: []string{root, "-type", "f", "-name", "*.toml", "-print"}})
	if err != nil {
		return nil, fmt.Errorf("discover confd metadata: %w", err)
	}
	destPattern := regexp.MustCompile(`(?m)^\s*dest\s*=\s*["']([^"']+)["']\s*$`)
	seen := map[string]struct{}{}
	for _, file := range strings.Split(output, "\n") {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}
		content, err := probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", file}})
		if err != nil {
			return nil, fmt.Errorf("read confd metadata %s: %w", file, err)
		}
		match := destPattern.FindStringSubmatch(content)
		if len(match) != 2 {
			continue
		}
		destination := strings.TrimSpace(match[1])
		if !strings.HasPrefix(destination, "/") || path.Clean(destination) != destination {
			return nil, fmt.Errorf("confd destination %q is not an absolute canonical path", destination)
		}
		seen[destination] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, errors.New("no confd generated destinations were discovered")
	}
	return result, nil
}
