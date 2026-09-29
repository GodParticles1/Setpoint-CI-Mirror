package xrocketnodes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"

	"setpoint/internal/deploymenttopology"
)

const (
	ProviderID        = "xrocket/package-nodes"
	maxNodesYAMLBytes = 512 << 10
)

var defaultPaths = []string{
	"/opt/data/xrocket/package/nodes.yaml",
	"/home/app/opt/data/xrocket/package/nodes.yaml",
}

type Provider struct {
	paths          []string
	readBounded    func(string) ([]byte, error)
	localAddresses func() ([]string, error)
}

func New() *Provider {
	return &Provider{
		paths:          append([]string(nil), defaultPaths...),
		readBounded:    readBoundedFile,
		localAddresses: interfaceAddresses,
	}
}

func newForTest(paths []string, readBounded func(string) ([]byte, error), localAddresses func() ([]string, error)) *Provider {
	return &Provider{paths: append([]string(nil), paths...), readBounded: readBounded, localAddresses: localAddresses}
}

func (provider *Provider) ID() string { return ProviderID }

func (provider *Provider) Collect(ctx context.Context) ([]deploymenttopology.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	localAddresses, localErr := provider.localAddresses()
	local := make(map[string]struct{}, len(localAddresses))
	if localErr == nil {
		for _, address := range localAddresses {
			local[address] = struct{}{}
		}
	}

	evidence := make([]deploymenttopology.Evidence, 0, len(provider.paths))
	for index, path := range provider.paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item := deploymenttopology.Evidence{
			ID:           fmt.Sprintf("xrocket-package-nodes-%02d", index),
			Source:       "xrocket/package-nodes:" + path,
			Confidence:   deploymenttopology.ConfidenceHigh,
			State:        deploymenttopology.EvidenceUnsupported,
			TopologyKind: deploymenttopology.KindUnknown,
		}

		content, err := provider.readBounded(path)
		if err != nil {
			item.Value = "source unavailable"
			if errors.Is(err, errFileTooLarge) {
				item.Value = "source exceeds bounded read limit"
			}
			evidence = append(evidence, item)
			continue
		}

		mode, participants, err := parseNodesYAML(content)
		if err != nil {
			item.Value = "source does not contain a supported explicit topology contract"
			evidence = append(evidence, item)
			continue
		}
		kind, ok := topologyKind(mode)
		if !ok {
			item.Value = "explicit install_mode is unsupported"
			evidence = append(evidence, item)
			continue
		}

		facts := make([]deploymenttopology.ParticipantFact, 0, len(participants))
		for _, address := range participants {
			_, isLocal := local[address]
			facts = append(facts, deploymenttopology.ParticipantFact{
				Key:       "ip:" + address,
				Addresses: []string{address},
				Local:     localErr == nil && isLocal,
			})
		}
		item.Value = fmt.Sprintf("install_mode=%s; participants=%d", mode, len(facts))
		item.State = deploymenttopology.EvidenceObserved
		item.TopologyKind = kind
		item.Participants = facts
		evidence = append(evidence, item)
	}
	return evidence, nil
}

var errFileTooLarge = errors.New("nodes.yaml exceeds bounded read limit")

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxNodesYAMLBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxNodesYAMLBytes {
		return nil, errFileTooLarge
	}
	return content, nil
}

func interfaceAddresses() ([]string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		value := address.String()
		if host, _, err := net.ParseCIDR(value); err == nil {
			result = append(result, host.String())
			continue
		}
		if host := net.ParseIP(value); host != nil {
			result = append(result, host.String())
		}
	}
	return uniqueSorted(result), nil
}

func topologyKind(mode string) (deploymenttopology.Kind, bool) {
	switch mode {
	case "single":
		return deploymenttopology.KindStandalone, true
	case "dual":
		return deploymenttopology.KindDual, true
	case "cluster":
		return deploymenttopology.KindCluster, true
	default:
		return deploymenttopology.KindUnknown, false
	}
}

func parseNodesYAML(content []byte) (string, []string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	mode := ""
	var participants []string
	nodesIndent := -1

	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), " \t\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := leadingIndent(raw)
		withoutList := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))

		if indent == 0 && strings.HasPrefix(trimmed, "install_mode:") {
			value := scalarValue(trimmed, "install_mode:")
			if value == "" || mode != "" {
				return "", nil, errors.New("install_mode must be unique and non-empty")
			}
			mode = value
		}

		if withoutList == "nodes:" {
			nodesIndent = indent
			continue
		}
		if nodesIndent < 0 {
			continue
		}
		if indent <= nodesIndent {
			nodesIndent = -1
			continue
		}
		if strings.HasPrefix(withoutList, "ip:") {
			value := scalarValue(withoutList, "ip:")
			address := net.ParseIP(value)
			if address == nil {
				return "", nil, errors.New("nodes entry has invalid ip")
			}
			participants = append(participants, address.String())
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, err
	}
	if mode == "" {
		return "", nil, errors.New("install_mode is missing")
	}
	return mode, uniqueSorted(participants), nil
}

func leadingIndent(value string) int {
	return len(value) - len(strings.TrimLeft(value, " \t"))
}

func scalarValue(line, prefix string) string {
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if index := strings.IndexByte(value, '#'); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return strings.Trim(value, "\"'")
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
