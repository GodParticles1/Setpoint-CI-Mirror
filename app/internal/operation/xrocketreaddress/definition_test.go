package xrocketreaddress

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

type discoveryExecutor struct {
	commands   []executor.Command
	versioned  bool
	keepalived string
}

func (fake *discoveryExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	fake.commands = append(fake.commands, command)
	switch {
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "address", "show"}):
		return executor.Result{Stdout: `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24,"scope":"global"},{"family":"inet","local":"192.0.2.12","prefixlen":24,"scope":"global"}]}]`}, nil
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "route", "show", "default"}):
		return executor.Result{Stdout: `[{"gateway":"192.0.2.1","dev":"eth0","prefsrc":"192.0.2.10","metric":100}]`}, nil
	case command.Name == "printenv" && reflect.DeepEqual(command.Args, []string{"HOME"}):
		return executor.Result{Stdout: "/root\n"}, nil
	case command.Name == "test" && len(command.Args) == 2 && command.Args[0] == "-e":
		if command.Args[1] == "/etc/keepalived/keepalived.conf" || command.Args[1] == "/opt/data/xrocket" {
			return executor.Result{ExitCode: 0}, nil
		}
		return missingResult()
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/keepalived/keepalived.conf"}):
		return executor.Result{Stdout: fake.keepalived}, nil
	case command.Name == "readlink" && reflect.DeepEqual(command.Args, []string{"-f", "--", "/opt/data/xrocket"}):
		resolved := "/opt/data/xrocket"
		if fake.versioned {
			resolved = "/opt/data/V300R004C68B009/xrocket"
		}
		return executor.Result{Stdout: resolved + "\n"}, nil
	default:
		return executor.Result{}, errors.New("unexpected command: " + command.Name + " " + strings.Join(command.Args, " "))
	}
}

func missingResult() (executor.Result, error) {
	result := executor.Result{ExitCode: 1}
	return result, &executor.Error{Kind: executor.ErrorExit, Result: result, Err: errors.New("not found")}
}

func validKeepalived() string {
	return `
vrrp_instance VI_1 {
    state MASTER
    interface eth0
    unicast_src_ip 192.0.2.10
    unicast_peer {
        192.0.2.11
    }
    virtual_ipaddress {
        192.0.2.12/24 dev eth0
    }
}`
}

func validParameters() []byte {
	return []byte(`{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","prefix_length":24,"gateway_address":"198.51.100.1"}`)
}

func runtimeInput(commandExecutor executor.CommandExecutor) operation.RuntimeInput {
	return operation.RuntimeInput{
		Executor: commandExecutor, Parameters: validParameters(), System: "linux",
		Targets: []operation.Target{{Kind: operation.TargetNode, NodeID: "node-master", SiteID: "site-1"}},
	}
}

func TestDiscoverCorrelatesNetworkKeepalivedVIPAndVersionEvidence(t *testing.T) {
	commandExecutor := &discoveryExecutor{versioned: true, keepalived: validKeepalived()}
	definition, err := NewDefinition(commandExecutor)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := definition.Discover(context.Background(), operation.DiscoverInput{Runtime: runtimeInput(commandExecutor)})
	if err != nil || !discovery.Applicable {
		t.Fatalf("discovery=%#v err=%v", discovery, err)
	}
	state, err := decodeDiscovery(discovery.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if state.MasterAddress != "192.0.2.10" || state.SlaveAddress != "192.0.2.11" || state.VIPAddress != "192.0.2.12" {
		t.Fatalf("topology=%#v", state)
	}
	if state.PrefixLength != 24 || state.GatewayAddress != "192.0.2.1" || state.Interface != "eth0" {
		t.Fatalf("network=%#v", state)
	}
	if state.ConfiguredRole != "MASTER" || state.RuntimeRole != "active_vip_owner" || state.ProductGeneration != "V300R004C68B009" {
		t.Fatalf("role/version=%#v", state)
	}
	precheck, err := definition.Precheck(context.Background(), operation.PrecheckInput{Runtime: runtimeInput(commandExecutor), Discovery: discovery})
	if err != nil || precheck.Passed || len(precheck.Findings) != 1 || precheck.Findings[0].Code != "APPLY_MECHANISM_UNVERIFIED" {
		t.Fatalf("precheck=%#v err=%v", precheck, err)
	}
	assertReadOnlyCommands(t, commandExecutor.commands)
}

func TestDiscoverFailsClosedWhenVersionEvidenceIsNotExact(t *testing.T) {
	commandExecutor := &discoveryExecutor{keepalived: validKeepalived()}
	definition, _ := NewDefinition(commandExecutor)
	discovery, err := definition.Discover(context.Background(), operation.DiscoverInput{Runtime: runtimeInput(commandExecutor)})
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Applicable {
		t.Fatalf("unversioned discovery was accepted: %#v", discovery)
	}
	state, err := decodeDiscovery(discovery.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(state.Unresolved, "xrocket_generation_version") {
		t.Fatalf("unresolved=%v", state.Unresolved)
	}
	assertReadOnlyCommands(t, commandExecutor.commands)
}

func TestExecutePlanningStopsAtEvidenceGapBeforePlanOrApply(t *testing.T) {
	commandExecutor := &discoveryExecutor{versioned: true, keepalived: validKeepalived()}
	definition, _ := NewDefinition(commandExecutor)
	result := operation.ExecutePlanning(context.Background(), definition, runtimeInput(commandExecutor), func() time.Time {
		return time.Unix(1_700_000_000, 0).UTC()
	})
	if result.State != operation.StateBlocked || result.Checkpoint != "precheck_blocked" || result.Precheck == nil {
		t.Fatalf("result=%#v", result)
	}
	if result.Plan != nil || result.Impact != nil || result.PlanDigest != "" {
		t.Fatalf("blocked planning produced executable material: %#v", result)
	}
	if result.Block == nil || result.Block.Code != "operation_precheck_blocked" {
		t.Fatalf("block=%#v", result.Block)
	}
	assertReadOnlyCommands(t, commandExecutor.commands)
}

func TestAllPostDiscoveryStagesFailClosedWithoutExecutingCommands(t *testing.T) {
	commandExecutor := &discoveryExecutor{versioned: true, keepalived: validKeepalived()}
	definition, _ := NewDefinition(commandExecutor)
	before := len(commandExecutor.commands)
	if _, err := definition.Plan(context.Background(), operation.PlanInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Plan err=%v", err)
	}
	if _, err := definition.Impact(context.Background(), operation.ImpactInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Impact err=%v", err)
	}
	if _, err := definition.Apply(context.Background(), operation.ApplyInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Apply err=%v", err)
	}
	if _, err := definition.Verify(context.Background(), operation.VerifyInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Verify err=%v", err)
	}
	if _, err := definition.Rollback(context.Background(), operation.RollbackInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Rollback err=%v", err)
	}
	if _, err := definition.VerifyRollback(context.Background(), operation.VerifyRollbackInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("VerifyRollback err=%v", err)
	}
	if len(commandExecutor.commands) != before {
		t.Fatalf("blocked stages executed commands: %#v", commandExecutor.commands[before:])
	}
}

func TestParameterNormalizationIsStrictAndCanonical(t *testing.T) {
	if err := operation.ValidateMetadata(Metadata()); err != nil {
		t.Fatalf("metadata is invalid: %v", err)
	}
	catalog := NewCatalogDescriptor()
	normalized, err := catalog.NormalizeParameters([]byte(`{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","prefix_length":"24","gateway_address":"198.51.100.1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["prefix_length"] != float64(24) {
		t.Fatalf("normalized=%s", normalized)
	}
	invalid := []string{
		`{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.10","vip_target_address":"198.51.100.12","prefix_length":24,"gateway_address":"198.51.100.1"}`,
		`{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.11","vip_target_address":"203.0.113.12","prefix_length":24,"gateway_address":"198.51.100.1"}`,
		`{"master_target_address":"198.51.100.10","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","prefix_length":24,"gateway_address":"198.51.100.1","extra":true}`,
	}
	for _, raw := range invalid {
		if _, err := catalog.NormalizeParameters([]byte(raw)); err == nil {
			t.Fatalf("invalid parameters accepted: %s", raw)
		}
	}
}

func TestKeepalivedParserRejectsAmbiguousOrIndirectTopology(t *testing.T) {
	for name, content := range map[string]string{
		"include":  "include /etc/keepalived/conf.d/*.conf\n" + validKeepalived(),
		"multiple": validKeepalived() + "\n" + strings.Replace(validKeepalived(), "VI_1", "VI_2", 1),
		"peer":     strings.Replace(validKeepalived(), "192.0.2.11", "192.0.2.11 192.0.2.13", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseKeepalivedConfig(content); err == nil {
				t.Fatal("ambiguous topology was accepted")
			}
		})
	}
}

func assertReadOnlyCommands(t *testing.T, commands []executor.Command) {
	t.Helper()
	allowed := map[string]bool{"ip": true, "printenv": true, "test": true, "cat": true, "readlink": true}
	for _, command := range commands {
		if !allowed[command.Name] {
			t.Fatalf("non-discovery command executed: %#v", command)
		}
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
