package xrocketreaddress

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

type keepalivedObservation struct {
	State         string
	Interface     string
	SourceAddress string
	PeerAddress   string
	VIPAddress    string
}

func parseKeepalivedConfig(content string) (keepalivedObservation, error) {
	if regexp.MustCompile(`(?im)^\s*include\b`).MatchString(content) {
		return keepalivedObservation{}, errors.New("include directives require version-specific expansion")
	}
	clean := stripKeepalivedComments(content)
	start := regexp.MustCompile(`(?i)\bvrrp_instance\s+[^\s{]+\s*\{`).FindStringIndex(clean)
	if start == nil {
		return keepalivedObservation{}, errors.New("vrrp_instance block is missing")
	}
	body, err := braceBody(clean, start[1]-1)
	if err != nil {
		return keepalivedObservation{}, err
	}
	if regexp.MustCompile(`(?i)\bvrrp_instance\s+`).MatchString(clean[start[1]:]) {
		return keepalivedObservation{}, errors.New("multiple vrrp_instance blocks are ambiguous")
	}
	result := keepalivedObservation{
		State:         strings.ToUpper(singleDirective(body, "state")),
		Interface:     singleDirective(body, "interface"),
		SourceAddress: canonicalIPv4(singleDirective(body, "unicast_src_ip")),
	}
	peerBody, peerErr := namedBlock(body, "unicast_peer")
	if peerErr != nil {
		return keepalivedObservation{}, peerErr
	}
	vipBody, vipErr := namedBlock(body, "virtual_ipaddress")
	if vipErr != nil {
		return keepalivedObservation{}, vipErr
	}
	result.PeerAddress, err = singleAddress(peerBody)
	if err != nil {
		return keepalivedObservation{}, fmt.Errorf("unicast_peer: %w", err)
	}
	result.VIPAddress, err = singleAddress(vipBody)
	if err != nil {
		return keepalivedObservation{}, fmt.Errorf("virtual_ipaddress: %w", err)
	}
	if result.State != "MASTER" && result.State != "BACKUP" {
		return keepalivedObservation{}, fmt.Errorf("unsupported configured state %q", result.State)
	}
	if result.Interface == "" || result.SourceAddress == "" {
		return keepalivedObservation{}, errors.New("interface or unicast_src_ip is missing")
	}
	return result, nil
}

func stripKeepalivedComments(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		if comment := strings.IndexAny(line, "#!"); comment >= 0 {
			line = line[:comment]
		}
		lines[index] = line
	}
	return strings.Join(lines, "\n")
}

func braceBody(content string, opening int) (string, error) {
	depth := 0
	for index := opening; index < len(content); index++ {
		switch content[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[opening+1 : index], nil
			}
		}
	}
	return "", errors.New("unterminated keepalived block")
}

func namedBlock(content, name string) (string, error) {
	matcher := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\s*\{`)
	match := matcher.FindStringIndex(content)
	if match == nil {
		return "", fmt.Errorf("%s block is missing", name)
	}
	return braceBody(content, match[1]-1)
}

func singleDirective(content, name string) string {
	matcher := regexp.MustCompile(`(?im)^\s*` + regexp.QuoteMeta(name) + `\s+([^\s{}]+)`)
	match := matcher.FindStringSubmatch(content)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func singleAddress(content string) (string, error) {
	var values []string
	for _, field := range strings.Fields(content) {
		candidate := strings.SplitN(field, "/", 2)[0]
		if parsed := net.ParseIP(candidate); parsed != nil && parsed.To4() != nil {
			values = append(values, parsed.To4().String())
		}
	}
	values = uniqueSorted(values)
	if len(values) != 1 {
		return "", fmt.Errorf("expected one IPv4 address, found %d", len(values))
	}
	return values[0], nil
}

func canonicalIPv4(value string) string {
	parsed := net.ParseIP(strings.SplitN(value, "/", 2)[0])
	if parsed == nil || parsed.To4() == nil {
		return ""
	}
	return parsed.To4().String()
}
