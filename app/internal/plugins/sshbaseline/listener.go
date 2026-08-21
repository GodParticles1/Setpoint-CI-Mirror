package sshbaseline

import (
	"bufio"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"setpoint/internal/plugins/checkutil"
	"setpoint/internal/task"
)

var listenerDefinitions = []checkutil.Definition{
	{
		ID: "ssh.listener.configured_ports_active", Name: "Configured SSH ports have active listeners",
		Recommended: "Every effective sshd port has a reliably attributed listener", Risk: "high",
		Description:         "An effective SSH port without a matching active sshd listener can make approved access assumptions inaccurate.",
		Remediation:         "Review effective SSH configuration and listener ownership before any controlled service change.",
		MayAffectConnection: true,
	},
	{
		ID: "ssh.listener.unexpected_ports", Name: "Unexpected SSH listener ports",
		Recommended: "No reliably attributed sshd listener exists outside the effective configured port set", Risk: "high",
		Description:         "An sshd listener outside the effective configured port set can expose an unreviewed access path.",
		Remediation:         "Confirm the intended access path and process ownership before any controlled service change.",
		MayAffectConnection: true,
	},
}

var processPattern = regexp.MustCompile(`"([^"]+)"[^)]*?pid=([0-9]+)`)

type listenerObservation struct {
	sshPorts        map[int]struct{}
	sshPIDs         map[int]struct{}
	unknownPorts    map[int]struct{}
	socketPorts     map[int]struct{}
	unsupportedRows int
}

func listenerItems(effective, listeners string, selected map[string]struct{}, now time.Time) ([]task.CheckItem, error) {
	configured, err := parseConfiguredPorts(effective)
	if err != nil {
		return listenerErrors(selected, "sshd_configured_ports_invalid", err, "Unable to parse complete effective SSH port configuration", now), err
	}
	observed := parseListeners(listeners)
	items := make([]task.CheckItem, 0, len(listenerDefinitions))
	for _, definition := range listenerDefinitions {
		if !checkSelected(selected, definition.ID) {
			continue
		}
		switch definition.ID {
		case "ssh.listener.configured_ports_active":
			items = append(items, configuredPortsItem(definition, configured, observed, now))
		case "ssh.listener.unexpected_ports":
			items = append(items, unexpectedPortsItem(definition, configured, observed, now))
		}
	}
	return items, nil
}

func parseConfiguredPorts(contents string) ([]int, error) {
	ports := map[int]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "port") {
			continue
		}
		if len(fields) != 2 {
			return nil, errors.New("sshd -T returned an unsupported Port record")
		}
		port, err := strconv.Atoi(fields[1])
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("sshd -T returned invalid port %q", fields[1])
		}
		ports[port] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan sshd -T output: %w", err)
	}
	if len(ports) == 0 {
		return nil, errors.New("sshd -T did not report an effective Port")
	}
	return sortedPorts(ports), nil
}

func parseListeners(contents string) listenerObservation {
	observation := listenerObservation{
		sshPorts: map[int]struct{}{}, sshPIDs: map[int]struct{}{},
		unknownPorts: map[int]struct{}{}, socketPorts: map[int]struct{}{},
	}
	scanner := bufio.NewScanner(strings.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || !strings.EqualFold(fields[0], "LISTEN") {
			observation.unsupportedRows++
			continue
		}
		port, ok := localPort(fields[3])
		if !ok {
			observation.unsupportedRows++
			continue
		}
		process := ""
		if len(fields) > 5 {
			process = strings.Join(fields[5:], " ")
		}
		matches := processPattern.FindAllStringSubmatch(process, -1)
		if len(matches) == 0 {
			observation.unknownPorts[port] = struct{}{}
			continue
		}
		classified := false
		for _, match := range matches {
			pid, err := strconv.Atoi(match[2])
			if err != nil {
				observation.unknownPorts[port] = struct{}{}
				continue
			}
			switch match[1] {
			case "sshd":
				observation.sshPorts[port] = struct{}{}
				observation.sshPIDs[pid] = struct{}{}
				classified = true
			case "systemd":
				observation.socketPorts[port] = struct{}{}
				classified = true
			default:
				classified = true
			}
		}
		if !classified {
			observation.unknownPorts[port] = struct{}{}
		}
	}
	if scanner.Err() != nil {
		observation.unsupportedRows++
	}
	return observation
}

func configuredPortsItem(definition checkutil.Definition, configured []int, observed listenerObservation, now time.Time) task.CheckItem {
	active := intersectPorts(configured, observed.sshPorts)
	current := fmt.Sprintf("configured=%s; active=%s", formatPorts(configured), formatPorts(active))
	evidence := listenerEvidence(configured, observed)
	if listenerSemanticsAmbiguous(observed) {
		return checkutil.ManualReview(definition, current, listenerReviewReason, evidence, now)
	}
	return checkutil.Value(definition, current, len(active) == len(configured), evidence, now)
}

func unexpectedPortsItem(definition checkutil.Definition, configured []int, observed listenerObservation, now time.Time) task.CheckItem {
	configuredSet := portSet(configured)
	unexpected := make([]int, 0)
	for port := range observed.sshPorts {
		if _, exists := configuredSet[port]; !exists {
			unexpected = append(unexpected, port)
		}
	}
	sort.Ints(unexpected)
	current := fmt.Sprintf("configured=%s; unexpected=%s", formatPorts(configured), formatPorts(unexpected))
	evidence := listenerEvidence(configured, observed)
	if listenerSemanticsAmbiguous(observed) {
		return checkutil.ManualReview(definition, current, listenerReviewReason, evidence, now)
	}
	return checkutil.Value(definition, current, len(unexpected) == 0, evidence, now)
}

const listenerReviewReason = "listener ownership is incomplete, socket-activated, or spans multiple sshd processes; automatic attribution is not reliable"

func listenerSemanticsAmbiguous(observed listenerObservation) bool {
	return len(observed.unknownPorts) > 0 || len(observed.socketPorts) > 0 || len(observed.sshPIDs) > 1 || observed.unsupportedRows > 0
}

func listenerEvidence(configured []int, observed listenerObservation) string {
	return fmt.Sprintf("sshd -T configured_count=%d; ss attributed_ssh_port_count=%d; ownership_complete=%t",
		len(configured), len(observed.sshPorts), !listenerSemanticsAmbiguous(observed))
}

func listenerErrors(selected map[string]struct{}, code string, err error, evidence string, now time.Time) []task.CheckItem {
	items := make([]task.CheckItem, 0, len(listenerDefinitions))
	for _, definition := range listenerDefinitions {
		if checkSelected(selected, definition.ID) {
			items = append(items, checkutil.Error(definition, code, err.Error(), evidence, now))
		}
	}
	return items
}

func anyListenerSelected(selected map[string]struct{}) bool {
	for _, definition := range listenerDefinitions {
		if checkSelected(selected, definition.ID) {
			return true
		}
	}
	return false
}

func localPort(address string) (int, bool) {
	index := strings.LastIndex(address, ":")
	if index < 0 || index == len(address)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(address[index+1:])
	return port, err == nil && port >= 1 && port <= 65535
}

func intersectPorts(configured []int, observed map[int]struct{}) []int {
	result := make([]int, 0, len(configured))
	for _, port := range configured {
		if _, exists := observed[port]; exists {
			result = append(result, port)
		}
	}
	return result
}

func portSet(ports []int) map[int]struct{} {
	result := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		result[port] = struct{}{}
	}
	return result
}

func sortedPorts(ports map[int]struct{}) []int {
	result := make([]int, 0, len(ports))
	for port := range ports {
		result = append(result, port)
	}
	sort.Ints(result)
	return result
}

func formatPorts(ports []int) string {
	values := make([]string, len(ports))
	for index, port := range ports {
		values[index] = strconv.Itoa(port)
	}
	return "[" + strings.Join(values, ",") + "]"
}
