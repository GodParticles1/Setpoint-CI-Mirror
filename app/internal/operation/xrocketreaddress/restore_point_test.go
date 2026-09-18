package xrocketreaddress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const restoreFixtureSecret = "restore-secret-must-not-persist"

type restoreFixtureExecutor struct {
	localAddress      string
	peerAddress       string
	vipOwner          bool
	priority          int
	memberID          uint64
	bootID            string
	omitBusinessPorts bool
	commands          []executor.Command
}

func (fake *restoreFixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	fake.commands = append(fake.commands, command)
	vipAddress := ""
	if fake.vipOwner {
		vipAddress = `,{"family":"inet","local":"192.0.2.12","prefixlen":24,"scope":"global"}`
	}
	switch {
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "address", "show"}):
		return executor.Result{Stdout: fmt.Sprintf(`[{"ifname":"eth0","addr_info":[{"family":"inet","local":"%s","prefixlen":24,"scope":"global"}%s]}]`, fake.localAddress, vipAddress)}, nil
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "route", "show", "default"}):
		return executor.Result{Stdout: fmt.Sprintf(`[{"gateway":"192.0.2.1","dev":"eth0","prefsrc":"%s","metric":100}]`, fake.localAddress)}, nil
	case command.Name == "printenv" && reflect.DeepEqual(command.Args, []string{"HOME"}):
		return executor.Result{Stdout: "/root\n"}, nil
	case command.Name == "test" && len(command.Args) == 2 && command.Args[0] == "-e":
		switch command.Args[1] {
		case "/etc/keepalived/keepalived.conf", "/opt/data/xrocket", "/etc/sysconfig/network-scripts/ifcfg-eth0", "/etc/confd/conf.d":
			return executor.Result{ExitCode: 0}, nil
		default:
			return missingRestoreResult()
		}
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/keepalived/keepalived.conf"}):
		return executor.Result{Stdout: fake.keepalived()}, nil
	case command.Name == "id" && reflect.DeepEqual(command.Args, []string{"-u"}):
		return executor.Result{Stdout: "0\n"}, nil
	case command.Name == "ps" && reflect.DeepEqual(command.Args, []string{"-eo", "user=,args="}):
		return executor.Result{Stdout: "root /opt/data/xrocket/xrocket.v2 start\n"}, nil
	case command.Name == "/opt/data/xrocket/xrocket.v2" && reflect.DeepEqual(command.Args, []string{"--version"}):
		return executor.Result{Stdout: "0.15.458\n"}, nil
	case command.Name == "find" && reflect.DeepEqual(command.Args, []string{"/etc", "-type", "f", "-name", "common.yaml", "-print"}):
		return executor.Result{Stdout: "/etc/confd/common.yaml\n"}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/confd/common.yaml"}):
		return executor.Result{Stdout: "goldendb:\n  address: 203.0.113.80\n  port: 8888\n  username: app\n  password: \"" + restoreFixtureSecret + "\"\n"}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/sysconfig/network-scripts/ifcfg-eth0"}):
		return executor.Result{Stdout: fmt.Sprintf("DEVICE=eth0\nIPADDR=%s\nPREFIX=24\nGATEWAY=192.0.2.1\n", fake.localAddress)}, nil
	case command.Name == "find" && reflect.DeepEqual(command.Args, []string{"/etc", "-type", "f", "-name", "etcd.conf", "-print"}):
		return executor.Result{Stdout: "/etc/etcd/etcd.conf\n"}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/etcd/etcd.conf"}):
		return executor.Result{Stdout: fmt.Sprintf("ETCD_ADVERTISE_CLIENT_URLS=http://%s:2379\nETCD_INITIAL_ADVERTISE_PEER_URLS=http://%s:2380\n", fake.localAddress, fake.localAddress)}, nil
	case command.Name == "find" && reflect.DeepEqual(command.Args, []string{"/opt", "-type", "f", "-name", "etcdctl", "-perm", "-111", "-print"}):
		return executor.Result{Stdout: "/opt/etcd/etcdctl\n"}, nil
	case command.Name == "env" && len(command.Args) == 5 && command.Args[0] == "ETCDCTL_API=3" && command.Args[1] == "/opt/etcd/etcdctl" && command.Args[3] == "endpoint" && command.Args[4] == "health":
		return executor.Result{Stdout: "healthy\n"}, nil
	case command.Name == "env" && len(command.Args) == 7 && command.Args[0] == "ETCDCTL_API=3" && command.Args[1] == "/opt/etcd/etcdctl" && command.Args[3] == "member" && command.Args[4] == "list" && command.Args[5] == "-w" && command.Args[6] == "json":
		return executor.Result{Stdout: fmt.Sprintf(`{"members":[{"ID":%d,"peerURLs":["http://%s:2380"]}]}`, fake.memberID, fake.localAddress)}, nil
	case command.Name == "monit" && reflect.DeepEqual(command.Args, []string{"summary"}):
		return executor.Result{Stdout: "'xetcd' Running\n'xnginx' Running\n"}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/proc/sys/kernel/random/boot_id"}):
		return executor.Result{Stdout: fake.bootID + "\n"}, nil
	case command.Name == "find" && reflect.DeepEqual(command.Args, []string{"/etc/confd/conf.d", "-type", "f", "-name", "*.toml", "-print"}):
		return executor.Result{Stdout: "/etc/confd/conf.d/xrocket.toml\n"}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/confd/conf.d/xrocket.toml"}):
		return executor.Result{Stdout: "[template]\nsrc = \"xrocket.tmpl\"\ndest = \"/etc/xrocket/rendered.conf\"\n"}, nil
	case command.Name == "readlink" && reflect.DeepEqual(command.Args, []string{"-f", "--", "/opt/data/xrocket"}):
		return executor.Result{Stdout: "/opt/data/V300R004C68B009/xrocket\n"}, nil
	default:
		return executor.Result{}, fmt.Errorf("unexpected restore command: %s %s", command.Name, strings.Join(command.Args, " "))
	}
}

func (fake *restoreFixtureExecutor) keepalived() string {
	business := "virtual_server 192.0.2.12 6000 {\n    delay_loop 3\n}\n"
	if fake.omitBusinessPorts {
		business = ""
	}
	return fmt.Sprintf(`
vrrp_instance VI_1 {
    state BACKUP
    interface eth0
    priority %d
    nopreempt
    unicast_src_ip %s
    unicast_peer {
        %s
    }
    virtual_ipaddress {
        192.0.2.12/24 dev eth0
    }
}
%s`, fake.priority, fake.localAddress, fake.peerAddress, business)
}

func missingRestoreResult() (executor.Result, error) {
	result := executor.Result{ExitCode: 1}
	return result, &executor.Error{Kind: executor.ErrorExit, Result: result, Err: errors.New("not found")}
}

func restoreMasterProfile() runtimeProfile {
	return runtimeProfile{
		InstallProfile:        installProfileRootNative,
		OSAuthorityUID:        0,
		ProductUser:           "root",
		ProductHome:           "/root",
		ProductPrefix:         "",
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
		EtcdctlPath:           "/opt/etcd/etcdctl",
		EtcdScheme:            "http",
		EtcdClientPort:        2379,
		EtcdPeerPort:          2380,
		EtcdMemberID:          "1234",
		EtcdMemberCount:       1,
		EtcdServiceName:       "xetcd",
		ServiceControlAdapter: "monit",
		BootID:                "boot-master",
		ConfdDestinations:     []string{"/etc/xrocket/rendered.conf"},
	}
}

func restorePlan(t *testing.T) operation.Plan {
	t.Helper()
	plan, err := buildPlan(precheckState{
		SchemaVersion: precheckSchema,
		Parameters: parameters{
			MasterTargetAddress:     "198.51.100.10",
			SlaveTargetAddress:      "198.51.100.11",
			VIPTargetAddress:        "198.51.100.12",
			ExternalDBTargetAddress: "203.0.113.20",
		},
		Discovery: discoveryState{
			SchemaVersion:          discoverySchema,
			NodeID:                 "node-master",
			MasterAddress:          "192.0.2.10",
			SlaveAddress:           "192.0.2.11",
			VIPAddress:             "192.0.2.12",
			PrefixLength:           24,
			GatewayAddress:         "192.0.2.1",
			Interface:              "eth0",
			ConfiguredRole:         "BACKUP",
			RuntimeRole:            "active_vip_owner",
			KeepalivedConfigPath:   "/etc/keepalived/keepalived.conf",
			KeepalivedPriority:     100,
			KeepalivedNopreempt:    true,
			BusinessPorts:          []int{6000},
			ProductVersionEvidence: "/opt/data/V300R004C68B009/xrocket",
			ProductGeneration:      "V300R004C68B009",
		},
		MasterProfile: restoreMasterProfile(),
		MasterNodeID:  "node-master",
		SlaveNodeID:   "node-slave",
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func restoreRequest(t *testing.T, role string) operation.RestorePointRequest {
	t.Helper()
	plan := restorePlan(t)
	stageIndex := 0
	if role == "master" {
		stageIndex = 1
	}
	stage := plan.Steps[stageIndex]
	return operation.RestorePointRequest{
		OperationID: OperationID,
		RunID:       "run-restore-001",
		Targets:     []operation.Target{stage.Target},
		Plan:        plan,
		Stage:       &stage,
		Retention:   30 * time.Minute,
	}
}

func TestRestorePointSeparatesMasterAndSlaveBeforeState(t *testing.T) {
	masterExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.10", peerAddress: "192.0.2.11", vipOwner: true, priority: 100, memberID: 4660, bootID: "boot-master"}
	masterProvider, err := NewRestorePointProvider(masterExecutor)
	if err != nil {
		t.Fatal(err)
	}
	masterPoint, err := masterProvider.Create(context.Background(), restoreRequest(t, "master"))
	if err != nil {
		t.Fatal(err)
	}
	masterManifest, err := decodeRestoreManifest(masterPoint)
	if err != nil {
		t.Fatal(err)
	}
	if masterManifest.OperationID != OperationID || masterManifest.OperationVersion != Metadata().Version || masterManifest.RunID != "run-restore-001" || masterManifest.NodeID != "node-master" || masterManifest.Role != "master" {
		t.Fatalf("master identity=%#v", masterManifest)
	}
	if !reflect.DeepEqual(masterManifest.ParticipantNodeIDs, []string{"node-master", "node-slave"}) {
		t.Fatalf("participants=%v", masterManifest.ParticipantNodeIDs)
	}
	if masterManifest.Before.Network.Address != "192.0.2.10" || masterManifest.Before.HA.SourceAddress != "192.0.2.10" || masterManifest.Before.HA.RuntimeRole != "active_vip_owner" || masterManifest.Before.Etcd.MemberID != "1234" {
		t.Fatalf("master before=%#v", masterManifest.Before)
	}
	if !reflect.DeepEqual(masterManifest.Before.Network.InterfaceAddresses, []restoreInterfaceAddress{{Address: "192.0.2.10", PrefixLength: 24}, {Address: "192.0.2.12", PrefixLength: 24}}) {
		t.Fatalf("master interface addresses=%#v", masterManifest.Before.Network.InterfaceAddresses)
	}

	slaveExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.11", peerAddress: "192.0.2.10", vipOwner: false, priority: 90, memberID: 4661, bootID: "boot-slave"}
	slaveProvider, err := NewRestorePointProvider(slaveExecutor)
	if err != nil {
		t.Fatal(err)
	}
	slavePoint, err := slaveProvider.Create(context.Background(), restoreRequest(t, "slave"))
	if err != nil {
		t.Fatal(err)
	}
	slaveManifest, err := decodeRestoreManifest(slavePoint)
	if err != nil {
		t.Fatal(err)
	}
	if slaveManifest.NodeID != "node-slave" || slaveManifest.Role != "slave" || slaveManifest.Before.Network.Address != "192.0.2.11" || slaveManifest.Before.HA.SourceAddress != "192.0.2.11" || slaveManifest.Before.HA.RuntimeRole != "standby" || slaveManifest.Before.Etcd.MemberID != "1235" {
		t.Fatalf("slave identity/before=%#v", slaveManifest)
	}
	if masterPoint.ID == slavePoint.ID || bytes.Equal(masterPoint.Manifest.Payload, slavePoint.Manifest.Payload) {
		t.Fatal("Master and Slave restore payloads were not separated")
	}
	if verification, err := masterProvider.Verify(context.Background(), masterPoint); err != nil || !verification.Passed {
		t.Fatalf("master verification=%#v err=%v", verification, err)
	}
	if verification, err := slaveProvider.Verify(context.Background(), slavePoint); err != nil || !verification.Passed {
		t.Fatalf("slave verification=%#v err=%v", verification, err)
	}
	assertRestoreCommandsReadOnly(t, masterExecutor.commands)
	assertRestoreCommandsReadOnly(t, slaveExecutor.commands)
}

func TestRestorePointManifestContainsOnlyBoundedOldStateAndNoSecrets(t *testing.T) {
	commandExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.10", peerAddress: "192.0.2.11", vipOwner: true, priority: 100, memberID: 4660, bootID: "boot-master"}
	provider, err := NewRestorePointProvider(commandExecutor)
	if err != nil {
		t.Fatal(err)
	}
	point, err := provider.Create(context.Background(), restoreRequest(t, "master"))
	if err != nil {
		t.Fatal(err)
	}
	payload := string(point.Manifest.Payload)
	for _, forbidden := range []string{
		"198.51.100.10", "198.51.100.11", "198.51.100.12", "203.0.113.20",
		restoreFixtureSecret, "password", "token", "private_key", "master_target_address", "slave_target_address", "vip_target_address", "external_db_target_address",
	} {
		if strings.Contains(strings.ToLower(payload), strings.ToLower(forbidden)) {
			t.Fatalf("restore payload contains forbidden new/secret material %q: %s", forbidden, payload)
		}
	}
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Before.Database.Address != "203.0.113.80" || manifest.Before.Database.Port != 8888 || manifest.Before.Product.CommonYAMLPath != "/etc/confd/common.yaml" || manifest.Before.Product.VersionEvidence != "/opt/data/V300R004C68B009/xrocket" {
		t.Fatalf("bounded old product/db state=%#v", manifest.Before)
	}
	if manifest.Before.Etcd.ClientAddress != "192.0.2.10" || manifest.Before.Etcd.ClientPort != 2379 || manifest.Before.Etcd.PeerAddress != "192.0.2.10" || manifest.Before.Etcd.PeerPort != 2380 || manifest.Before.Etcd.MemberCount != 1 {
		t.Fatalf("bounded old etcd state=%#v", manifest.Before.Etcd)
	}
	if !reflect.DeepEqual(manifest.Before.Rendering.ConfdDestinations, []string{"/etc/xrocket/rendered.conf"}) {
		t.Fatalf("rendering=%#v", manifest.Before.Rendering)
	}
	assertRestoreCommandsReadOnly(t, commandExecutor.commands)
}

func TestRestorePointFailsClosedOnIdentityDriftOrMissingRecoveryFacts(t *testing.T) {
	t.Run("stage identity", func(t *testing.T) {
		commandExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.10", peerAddress: "192.0.2.11", vipOwner: true, priority: 100, memberID: 4660, bootID: "boot-master"}
		provider, _ := NewRestorePointProvider(commandExecutor)
		request := restoreRequest(t, "master")
		tampered := *request.Stage
		tampered.ExecutorNodeID = "node-slave"
		request.Stage = &tampered
		if _, err := provider.Create(context.Background(), request); err == nil {
			t.Fatal("tampered frozen stage was accepted")
		}
		if len(commandExecutor.commands) != 0 {
			t.Fatalf("identity failure executed local commands: %#v", commandExecutor.commands)
		}
	})

	t.Run("master profile drift", func(t *testing.T) {
		commandExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.10", peerAddress: "192.0.2.11", vipOwner: true, priority: 100, memberID: 4660, bootID: "boot-master-after-reboot"}
		provider, _ := NewRestorePointProvider(commandExecutor)
		if _, err := provider.Create(context.Background(), restoreRequest(t, "master")); err == nil || !strings.Contains(err.Error(), "drifted") {
			t.Fatalf("master profile drift err=%v", err)
		}
		assertRestoreCommandsReadOnly(t, commandExecutor.commands)
	})

	t.Run("missing HA ports", func(t *testing.T) {
		commandExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.11", peerAddress: "192.0.2.10", priority: 90, memberID: 4661, bootID: "boot-slave", omitBusinessPorts: true}
		provider, _ := NewRestorePointProvider(commandExecutor)
		if _, err := provider.Create(context.Background(), restoreRequest(t, "slave")); err == nil || !strings.Contains(err.Error(), "recovery facts are incomplete") {
			t.Fatalf("missing HA facts err=%v", err)
		}
		assertRestoreCommandsReadOnly(t, commandExecutor.commands)
	})
}

func TestRestorePointRollbackMethodsRemainFailClosedWithoutMutation(t *testing.T) {
	commandExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.10", peerAddress: "192.0.2.11", vipOwner: true, priority: 100, memberID: 4660, bootID: "boot-master"}
	providerInterface, _ := NewRestorePointProvider(commandExecutor)
	provider := providerInterface.(*restorePointProvider)
	point, err := provider.Create(context.Background(), restoreRequest(t, "master"))
	if err != nil {
		t.Fatal(err)
	}
	before := len(commandExecutor.commands)
	if _, err := provider.Restore(context.Background(), point, operation.ApplyResult{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Restore err=%v", err)
	}
	if _, err := provider.VerifyRestored(context.Background(), point, operation.RollbackResult{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("VerifyRestored err=%v", err)
	}
	if len(commandExecutor.commands) != before {
		t.Fatalf("fail-closed rollback methods executed commands: %#v", commandExecutor.commands[before:])
	}
}

func assertRestoreCommandsReadOnly(t *testing.T, commands []executor.Command) {
	t.Helper()
	for _, command := range commands {
		switch command.Name {
		case "ip":
			if !reflect.DeepEqual(command.Args, []string{"-j", "address", "show"}) && !reflect.DeepEqual(command.Args, []string{"-j", "route", "show", "default"}) {
				t.Fatalf("unexpected ip mutation-capable command: %#v", command)
			}
		case "printenv":
			if !reflect.DeepEqual(command.Args, []string{"HOME"}) {
				t.Fatalf("unexpected printenv command: %#v", command)
			}
		case "test":
			if len(command.Args) != 2 || command.Args[0] != "-e" {
				t.Fatalf("unexpected test command: %#v", command)
			}
		case "cat":
			if len(command.Args) != 2 || command.Args[0] != "--" {
				t.Fatalf("unexpected cat command: %#v", command)
			}
		case "id":
			if !reflect.DeepEqual(command.Args, []string{"-u"}) {
				t.Fatalf("unexpected id command: %#v", command)
			}
		case "ps":
			if !reflect.DeepEqual(command.Args, []string{"-eo", "user=,args="}) {
				t.Fatalf("unexpected ps command: %#v", command)
			}
		case "/opt/data/xrocket/xrocket.v2":
			if !reflect.DeepEqual(command.Args, []string{"--version"}) {
				t.Fatalf("xrocket mutation command executed: %#v", command)
			}
		case "find":
			if len(command.Args) < 5 || command.Args[1] != "-type" || command.Args[2] != "f" || command.Args[len(command.Args)-1] != "-print" {
				t.Fatalf("unexpected find command: %#v", command)
			}
		case "env":
			joined := strings.Join(command.Args, " ")
			if !strings.HasPrefix(joined, "ETCDCTL_API=3 /opt/etcd/etcdctl --endpoints=http://") || (!strings.HasSuffix(joined, " endpoint health") && !strings.HasSuffix(joined, " member list -w json")) {
				t.Fatalf("etcd mutation command executed: %#v", command)
			}
		case "monit":
			if !reflect.DeepEqual(command.Args, []string{"summary"}) {
				t.Fatalf("Monit mutation command executed: %#v", command)
			}
		case "readlink":
			if !reflect.DeepEqual(command.Args, []string{"-f", "--", "/opt/data/xrocket"}) {
				t.Fatalf("unexpected readlink command: %#v", command)
			}
		default:
			t.Fatalf("unexpected command in RestorePoint capture: %#v", command)
		}
	}
}
