package xrocketreaddress

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

type noCommandExecutor struct{}

func (noCommandExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	panic("planning fixture must not execute a command")
}

func boundedDiscovery() discoveryState {
	return discoveryState{
		SchemaVersion:          discoverySchema,
		NodeID:                 "node-master",
		ProductGeneration:      "V300R004C68B009",
		ProductVersionEvidence: "/opt/data/V300R004C68B009/xrocket",
		ConfiguredRole:         "BACKUP",
		RuntimeRole:            "active_vip_owner",
		MasterAddress:          "192.0.2.10",
		SlaveAddress:           "192.0.2.11",
		VIPAddress:             "192.0.2.12",
		PrefixLength:           24,
		GatewayAddress:         "192.0.2.1",
		Interface:              "eth0",
		KeepalivedConfigPath:   "/etc/keepalived/keepalived.conf",
		KeepalivedPriority:     100,
		KeepalivedNopreempt:    true,
		BusinessPorts:          []int{6000, 7001},
	}
}

func boundedProfile() runtimeProfile {
	return runtimeProfile{
		InstallProfile:        installProfileRootNative,
		OSAuthorityUID:        0,
		ProductUser:           "root",
		ProductHome:           "/root",
		XrocketBinary:         "/opt/data/xrocket/xrocket.v2",
		XrocketVersion:        "0.15.458",
		CommonYAMLPath:        "/etc/confd/common.yaml",
		ExternalDBAddress:     "203.0.113.80",
		ExternalDBPort:        8888,
		NetworkBackend:        "ifcfg",
		NetworkConfigPath:     "/etc/sysconfig/network-scripts/ifcfg-eth0",
		PersistentAddress:     "192.0.2.10",
		PersistentPrefix:      24,
		PersistentGateway:     "192.0.2.1",
		EtcdConfigPath:        "/etc/etcd/etcd.conf",
		EtcdctlPath:           "/opt/bin/etcdctl",
		EtcdScheme:            "http",
		EtcdClientPort:        12379,
		EtcdPeerPort:          12380,
		EtcdMemberID:          "1234",
		EtcdMemberCount:       1,
		EtcdServiceName:       "xetcd",
		ServiceControlAdapter: "monit",
		BootID:                "boot-before",
		ConfdDestinations:     []string{"/etc/generated/a.conf"},
	}
}

func boundedRuntime(parameters string) operation.RuntimeInput {
	return operation.RuntimeInput{
		Executor:   noCommandExecutor{},
		Parameters: json.RawMessage(parameters),
		System:     "linux",
		Targets: []operation.Target{
			{Kind: operation.TargetNode, NodeID: "node-master", SiteID: "site-1"},
			{Kind: operation.TargetNode, NodeID: "node-slave", SiteID: "site-1"},
		},
	}
}

func boundedParametersJSON() string {
	return `{"master_target_address":"192.0.2.20","slave_target_address":"192.0.2.21","vip_target_address":"192.0.2.22","external_db_target_address":"203.0.113.81"}`
}

func TestTwoAgentPrecheckBuildsSnapshotFirstStagedPlan(t *testing.T) {
	definition := &Definition{profileDiscover: func(context.Context, discoveryState) (runtimeProfile, error) {
		return boundedProfile(), nil
	}}
	discoveryState := boundedDiscovery()
	discoveryArtifact, err := encodeArtifact(discoverySchema, discoveryState)
	if err != nil {
		t.Fatal(err)
	}
	runtime := boundedRuntime(boundedParametersJSON())
	precheck, err := definition.Precheck(context.Background(), operation.PrecheckInput{
		Runtime:   runtime,
		Discovery: operation.Discovery{Applicable: true, Snapshot: discoveryArtifact, Targets: runtime.Targets},
	})
	if err != nil || !precheck.Passed {
		t.Fatalf("precheck=%#v err=%v", precheck, err)
	}
	plan, err := definition.Plan(context.Background(), operation.PlanInput{Runtime: runtime, Precheck: precheck})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != planSchema || len(plan.Steps) != 15 {
		t.Fatalf("plan schema=%q steps=%d", plan.SchemaVersion, len(plan.Steps))
	}
	ids := make([]string, len(plan.Steps))
	for index := range plan.Steps {
		ids[index] = plan.Steps[index].ID
	}
	expected := []string{
		"snapshot-slave", "snapshot-master", "alias-slave", "alias-master",
		"product-slave", "product-master", "external-db-slave", "external-db-master",
		"etcd-slave", "etcd-master", "confd-slave", "confd-master",
		"os-slave", "os-master", "final-slave",
	}
	if !reflect.DeepEqual(ids, expected) {
		t.Fatalf("stage order=%v", ids)
	}
	if plan.Steps[0].Writes || plan.Steps[1].Writes {
		t.Fatal("both RestorePoint snapshot stages must complete before the first mutation")
	}
	if !plan.Steps[2].Writes {
		t.Fatal("first mutation must occur only after both snapshot stages")
	}
	if plan.Steps[12].ExecutorNodeID != "node-slave" || plan.Steps[12].Barrier != operation.StageBarrierAgentReconnect {
		t.Fatalf("Slave reboot stage=%#v", plan.Steps[12])
	}
	if plan.Steps[13].ExecutorNodeID != "node-master" || plan.Steps[13].Barrier != operation.StageBarrierAgentReconnect {
		t.Fatalf("Master reboot stage=%#v", plan.Steps[13])
	}
	if plan.Steps[14].ExecutorNodeID != "node-slave" || plan.Steps[14].Writes {
		t.Fatalf("final verification stage=%#v", plan.Steps[14])
	}
	impact, err := definition.Impact(context.Background(), operation.ImpactInput{Runtime: runtime, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if impact.Risk != operation.RiskCritical || !impact.RequiresDowntime || len(impact.Changes) != 4 {
		t.Fatalf("impact=%#v", impact)
	}
}

func TestBoundedPrecheckRejectsCrossSubnetAndDBCollisions(t *testing.T) {
	state := boundedDiscovery()
	profile := boundedProfile()
	cases := []struct {
		name  string
		value parameters
	}{
		{
			name:  "cross-subnet",
			value: parameters{MasterTargetAddress: "198.51.100.20", SlaveTargetAddress: "192.0.2.21", VIPTargetAddress: "192.0.2.22", ExternalDBTargetAddress: "203.0.113.81"},
		},
		{
			name:  "db-target-overlaps-site",
			value: parameters{MasterTargetAddress: "192.0.2.20", SlaveTargetAddress: "192.0.2.21", VIPTargetAddress: "192.0.2.22", ExternalDBTargetAddress: "192.0.2.20"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validateBoundedPrecheck(test.value, state, profile); err == nil {
				t.Fatal("invalid bounded topology was accepted")
			}
		})
	}
}
