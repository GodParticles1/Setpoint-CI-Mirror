//go:build linux

package xrocketreaddress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

func (fs *mappedRecoveryFilesystem) Chown(name string, uid, gid int) error {
	return os.Chown(fs.actual(name), uid, gid)
}

type mutationAdapterFixtureExecutor struct {
	commands     []executor.Command
	failMatch    string
	peer         string
	bootID       string
	nextBoot     string
	healthOutput string
	memberID     uint64
	memberCount  int
	kvJSON       string
	addressJSON  string
	routeJSON    string
}

func newMutationAdapterFixtureExecutor() *mutationAdapterFixtureExecutor {
	return &mutationAdapterFixtureExecutor{
		peer:         "192.0.2.10",
		bootID:       "boot-forward",
		nextBoot:     "boot-after",
		healthOutput: "endpoint is healthy\n",
		memberID:     0x1234,
		memberCount:  1,
		kvJSON:       kvFixtureJSON([][2]string{{"alpha", "secret-one"}, {"beta", "secret-two"}}),
		addressJSON:  `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24,"scope":"global"}]}]`,
		routeJSON:    `[{"gateway":"192.0.2.1","dev":"eth0","prefsrc":"192.0.2.10","metric":100}]`,
	}
}

func (fake *mutationAdapterFixtureExecutor) Execute(ctx context.Context, command executor.Command) (executor.Result, error) {
	if err := ctx.Err(); err != nil {
		return executor.Result{}, err
	}
	fake.commands = append(fake.commands, command)
	signature := command.Name + " " + strings.Join(command.Args, " ")
	if fake.failMatch != "" && strings.Contains(signature, fake.failMatch) {
		result := executor.Result{ExitCode: 1}
		return result, &executor.Error{Kind: executor.ErrorExit, Result: result, Err: errors.New("fixture failure")}
	}
	switch {
	case command.Name == "env" && len(command.Args) >= 7 && command.Args[3] == "member" && command.Args[4] == "update":
		for _, argument := range command.Args {
			if strings.HasPrefix(argument, "--peer-urls=") {
				_, address, _, err := oneHTTPURL(strings.TrimPrefix(argument, "--peer-urls="))
				if err != nil {
					return executor.Result{}, err
				}
				fake.peer = address
			}
		}
		return executor.Result{Stdout: "member updated\n"}, nil
	case command.Name == "env" && len(command.Args) == 5 && command.Args[3] == "endpoint" && command.Args[4] == "health":
		return executor.Result{Stdout: fake.healthOutput}, nil
	case command.Name == "env" && len(command.Args) == 7 && command.Args[3] == "member" && command.Args[4] == "list":
		members := make([]map[string]any, 0, fake.memberCount)
		for index := 0; index < fake.memberCount; index++ {
			members = append(members, map[string]any{
				"ID": fake.memberID + uint64(index), "peerURLs": []string{"http://" + fake.peer + ":2380"},
			})
		}
		payload, _ := json.Marshal(map[string]any{"members": members})
		return executor.Result{Stdout: string(payload)}, nil
	case command.Name == "env" && len(command.Args) == 8 && command.Args[3] == "get" && command.Args[5] == "--prefix":
		return executor.Result{Stdout: fake.kvJSON}, nil
	case command.Name == "monit" && reflect.DeepEqual(command.Args, []string{"restart", "xetcd"}):
		return executor.Result{}, nil
	case command.Name == "reboot" && len(command.Args) == 0:
		fake.bootID = fake.nextBoot
		return executor.Result{}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/proc/sys/kernel/random/boot_id"}):
		return executor.Result{Stdout: fake.bootID + "\n"}, nil
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "address", "show"}):
		return executor.Result{Stdout: fake.addressJSON}, nil
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "route", "show", "default"}):
		return executor.Result{Stdout: fake.routeJSON}, nil
	case command.Name == "ip" && len(command.Args) >= 5 && command.Args[0] == "address":
		return executor.Result{}, nil
	case command.Name == "/opt/data/xrocket/xrocket.v2":
		return executor.Result{}, nil
	default:
		return executor.Result{}, fmt.Errorf("unexpected mutation fixture command: %s", signature)
	}
}

func mutationAdapterFixture(fs *mappedRecoveryFilesystem, fake *mutationAdapterFixtureExecutor) *productionMutationAdapter {
	return &productionMutationAdapter{
		executor: fake,
		fs:       fs,
		sleep:    func(context.Context, time.Duration) error { return nil },
	}
}

func captureRecoveryForStage(t *testing.T, fs *mappedRecoveryFilesystem, before restoreBeforeState, stageIndex int) (RecoveryArtifactCaptureRequest, RecoveryArtifactContract) {
	t.Helper()
	request := productionCaptureRequest(t, stageIndex, before)
	collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
	recovery, err := collector.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return request, recovery
}

func rollbackExpectationFixture(t *testing.T, request RecoveryArtifactCaptureRequest, recovery RecoveryArtifactContract) rollbackStageExpectation {
	t.Helper()
	manifest := restorePointManifest{
		SchemaVersion:      RestorePointRollbackSchema,
		OperationID:        OperationID,
		OperationVersion:   Metadata().Version,
		RunID:              request.Owner.RunID,
		StageID:            request.Owner.StageID,
		StageIndex:         request.Owner.StageIndex,
		NodeID:             request.Owner.NodeID,
		ParticipantNodeIDs: append([]string(nil), request.Owner.ParticipantNodeIDs...),
		Role:               request.Owner.Role,
		Before:             request.Before,
		Recovery:           &recovery,
	}
	expectation, err := buildRollbackExpectation(manifest, canonicalStages[request.Owner.StageIndex])
	if err != nil {
		t.Fatal(err)
	}
	return expectation
}

func TestProductionMutationForwardMethods(t *testing.T) {
	t.Run("alias", func(t *testing.T) {
		fake := newMutationAdapterFixtureExecutor()
		adapter := mutationAdapterFixture(newMappedRecoveryFilesystem(t), fake)
		contract := AliasStageContract{
			NodeID: "node-master", Role: "master", Interface: "eth0",
			OldAddress: "192.0.2.10", NewAddress: "198.51.100.10", PrefixLength: 24, Gateway: "192.0.2.1",
		}
		receipt, err := adapter.AddTargetAlias(context.Background(), contract)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		want := executor.Command{
			Name: "ip", Args: []string{"address", "add", "198.51.100.10/24", "dev", "eth0"},
			OutputLimit: mutationCommandOutputLimit,
		}
		if len(fake.commands) != 3 || !reflect.DeepEqual(fake.commands[2], want) {
			t.Fatalf("commands=%#v want_last=%#v", fake.commands, want)
		}
	})

	t.Run("product", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		_, recovery := captureRecoveryForStage(t, fs, before, 5)
		bundle, _ := recoveryArtifactByKindSource(recovery, recoveryKindProductBundle, before.Product.VersionEvidence)
		keepalived, _ := recoveryArtifactByKindSource(recovery, recoveryKindKeepalivedConfig, before.HA.ConfigPath)
		contract := ProductStageContract{
			NodeID: "node-master", Role: "master", InstallProfile: installProfileRootNative,
			ProductUser: "root", ProductHome: "/root", XrocketBinary: "/opt/data/xrocket/xrocket.v2",
			OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress,
			NewMaster: "198.51.100.10", NewSlave: "198.51.100.11", NewVIP: "198.51.100.12",
			ProductBundle: bundle, KeepalivedConfig: keepalived,
		}
		fake := newMutationAdapterFixtureExecutor()
		receipt, err := mutationAdapterFixture(fs, fake).ReaddressProduct(context.Background(), contract)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		want := []string{"updateIP", "dual", "--oldM=192.0.2.10", "--oldS=192.0.2.11", "--oldV=192.0.2.12", "--newM=198.51.100.10", "--newS=198.51.100.11", "--newV=198.51.100.12"}
		if len(fake.commands) != 1 || fake.commands[0].Name != contract.XrocketBinary || !reflect.DeepEqual(fake.commands[0].Args, want) {
			t.Fatalf("product command=%#v", fake.commands)
		}
		denied := contract
		denied.InstallProfile = installProfileHomePrefix
		denied.ProductUser, denied.ProductHome, denied.ProductPrefix = "xbrother", "/home/xbrother", "/home/xbrother"
		denied.XrocketBinary = "/home/xbrother/opt/data/xrocket/xrocket.v2"
		if _, err := mutationAdapterFixture(fs, newMutationAdapterFixtureExecutor()).ReaddressProduct(context.Background(), denied); err == nil {
			t.Fatal("HOME-prefixed production mutation was accepted")
		}
	})

	t.Run("external DB", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		_, recovery := captureRecoveryForStage(t, fs, before, 7)
		baseline, _ := recoveryArtifactByKindSource(recovery, recoveryKindCommonYAML, before.Product.CommonYAMLPath)
		contract := ExternalDBStageContract{
			NodeID: "node-master", Role: "master", CommonYAMLPath: before.Product.CommonYAMLPath,
			OldAddress: before.Database.Address, NewAddress: "203.0.113.20", Port: before.Database.Port,
			PreserveNonAddressFields: true, BaselineConfig: baseline,
		}
		receipt, err := mutationAdapterFixture(fs, newMutationAdapterFixtureExecutor()).UpdateExternalDB(context.Background(), contract)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		current := stringMustRead(t, fs.actual(before.Product.CommonYAMLPath))
		if !strings.Contains(current, "address: 203.0.113.20") || !strings.Contains(current, "password: do-not-log") || strings.Contains(fmt.Sprintf("%#v", receipt), "do-not-log") {
			t.Fatalf("external DB boundary failed: current=%s receipt=%#v", current, receipt)
		}
	})

	t.Run("etcd", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		contract := EtcdStageContract{
			NodeID: "node-master", Role: "master", ConfigPath: before.Etcd.ConfigPath,
			EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme,
			MemberID: before.Etcd.MemberID, MemberCount: 1,
			OldClient: before.Etcd.ClientAddress, NewClient: "198.51.100.10", ClientPort: before.Etcd.ClientPort,
			OldPeer: before.Etcd.PeerAddress, NewPeer: "198.51.100.10", PeerPort: before.Etcd.PeerPort,
			ServiceName: "xetcd", ControlAdapter: "monit",
		}
		fake := newMutationAdapterFixtureExecutor()
		digest, digestErr := deterministicKVDigest([]byte(fake.kvJSON))
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		contract.LogicalKVDigest = digest
		receipt, err := mutationAdapterFixture(fs, fake).ReaddressEtcd(context.Background(), contract)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		foundUpdate := false
		for _, command := range fake.commands {
			if command.Name == "env" && reflect.DeepEqual(command.Args, []string{"ETCDCTL_API=3", before.Etcd.EtcdctlPath, "--endpoints=http://192.0.2.10:2379", "member", "update", "1234", "--peer-urls=http://198.51.100.10:2380"}) {
				foundUpdate = true
			}
		}
		if !foundUpdate {
			t.Fatalf("etcd commands=%#v", fake.commands)
		}
		current := stringMustRead(t, fs.actual(before.Etcd.ConfigPath))
		if strings.Contains(current, "192.0.2.10") || !strings.Contains(current, "198.51.100.10") {
			t.Fatalf("etcd config=%s", current)
		}
	})

	t.Run("confd", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		_, recovery := captureRecoveryForStage(t, fs, before, 11)
		contract := ConfdStageContract{
			NodeID: "node-master", Role: "master", InstallProfile: installProfileRootNative,
			ProductUser: "root", ProductHome: "/root", XrocketBinary: "/opt/data/xrocket/xrocket.v2",
			Destinations: append([]string(nil), before.Rendering.ConfdDestinations...),
			DestinationFiles: append([]RecoveryArtifactRef(nil), recovery.Artifacts...),
			OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, OldExternalDB: before.Database.Address,
			ExpectedMaster: "198.51.100.10", ExpectedSlave: "198.51.100.11", ExpectedVIP: "198.51.100.12", ExpectedExternalDB: "203.0.113.20",
		}
		fake := newMutationAdapterFixtureExecutor()
		if _, err := mutationAdapterFixture(fs, fake).RenderConfd(context.Background(), contract); err != nil {
			t.Fatal(err)
		}
		if len(fake.commands) != 1 || fake.commands[0].Name != contract.XrocketBinary || !reflect.DeepEqual(fake.commands[0].Args, []string{"confd"}) {
			t.Fatalf("confd command=%#v", fake.commands)
		}
	})

	t.Run("OS", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		contract := OSStageContract{
			NodeID: "node-master", Role: "master", Interface: "eth0", ConfigPath: before.Network.ConfigPath,
			OldAddress: before.Network.PersistentAddress, NewAddress: "198.51.100.10",
			PrefixLength: before.Network.PersistentPrefix, Gateway: before.Network.PersistentGateway,
			InterfaceAddresses: append([]restoreInterfaceAddress(nil), before.Network.InterfaceAddresses...),
			BootIDBefore: "boot-before", Barrier: operation.StageBarrierAgentReconnect, Reboot: true,
		}
		fake := newMutationAdapterFixtureExecutor()
		fake.bootID = "boot-before"
		if _, err := mutationAdapterFixture(fs, fake).CutoverOS(context.Background(), contract); err != nil {
			t.Fatal(err)
		}
		current := stringMustRead(t, fs.actual(before.Network.ConfigPath))
		if !strings.Contains(current, "IPADDR=198.51.100.10") || !strings.Contains(current, "PREFIX=24") || !strings.Contains(current, "GATEWAY=192.0.2.1") {
			t.Fatalf("ifcfg=%s", current)
		}
		for _, command := range fake.commands {
			if command.Name == "reboot" {
				t.Fatalf("adapter must not reboot before TaskWorker journals the reconnect handoff: %#v", fake.commands)
			}
		}
	})
}

func TestProductionRollbackMutationUsesRecoveryArtifacts(t *testing.T) {
	t.Run("alias exact baseline and runtime address restore", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 3)
		expectation := rollbackExpectationFixture(t, request, recovery)
		fake := newMutationAdapterFixtureExecutor()
		fake.addressJSON = `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24,"scope":"global"},{"family":"inet","local":"198.51.100.10","prefixlen":24,"scope":"global"}]}]`
		receipt, err := mutationAdapterFixture(fs, fake).RestoreStage(context.Background(), expectation)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		foundDelete := false
		for _, command := range fake.commands {
			if command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"address", "del", "198.51.100.10/24", "dev", "eth0"}) {
				foundDelete = true
			}
		}
		if !foundDelete {
			t.Fatalf("alias rollback did not remove temporary target alias: %#v", fake.commands)
		}
	})

	t.Run("product does not reverse updateIP", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 5)
		expectation := rollbackExpectationFixture(t, request, recovery)
		writePositiveProductTransition(t, fs, before)
		fake := newMutationAdapterFixtureExecutor()
		receipt, err := mutationAdapterFixture(fs, fake).RestoreStage(context.Background(), expectation)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		for _, command := range fake.commands {
			if command.Name == "/opt/data/xrocket/xrocket.v2" && len(command.Args) >= 2 && command.Args[0] == "updateIP" {
				t.Fatalf("rollback invoked reverse updateIP: %#v", command)
			}
		}
	})

	t.Run("external DB exact restore", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 7)
		expectation := rollbackExpectationFixture(t, request, recovery)
		current := strings.ReplaceAll(stringMustRead(t, fs.actual(before.Product.CommonYAMLPath)), before.Database.Address, "203.0.113.20")
		fs.writeFile(t, before.Product.CommonYAMLPath, current, 0o640)
		if _, err := mutationAdapterFixture(fs, newMutationAdapterFixtureExecutor()).RestoreStage(context.Background(), expectation); err != nil {
			t.Fatal(err)
		}
		restored := stringMustRead(t, fs.actual(before.Product.CommonYAMLPath))
		if !strings.Contains(restored, "address: 203.0.113.80") || !strings.Contains(restored, "password: do-not-log") {
			t.Fatalf("restored common.yaml=%s", restored)
		}
	})

	t.Run("etcd snapshot is escrow only and member returns to old peer", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 9)
		expectation := rollbackExpectationFixture(t, request, recovery)
		fs.writeFile(t, before.Etcd.ConfigPath, "ETCD_ADVERTISE_CLIENT_URLS=http://198.51.100.10:2379\nETCD_INITIAL_ADVERTISE_PEER_URLS=http://198.51.100.10:2380\n", 0o640)
		fake := newMutationAdapterFixtureExecutor()
		fake.peer = "198.51.100.10"
		receipt, err := mutationAdapterFixture(fs, fake).RestoreStage(context.Background(), expectation)
		if err != nil || !validSHA256Digest(receipt.Digest) {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		foundRestoreSnapshot := false
		for _, command := range fake.commands {
			if strings.Contains(strings.Join(command.Args, " "), "snapshot restore") {
				foundRestoreSnapshot = true
			}
		}
		if foundRestoreSnapshot {
			t.Fatal("rollback invented etcd snapshot restore")
		}
		if !strings.Contains(stringMustRead(t, fs.actual(before.Etcd.ConfigPath)), "192.0.2.10") {
			t.Fatal("etcd old config was not restored")
		}
	})

	t.Run("confd exact restore", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 11)
		expectation := rollbackExpectationFixture(t, request, recovery)
		fs.writeFile(t, before.Rendering.ConfdDestinations[0], "master=198.51.100.10 mode=drift\n", 0o640)
		if _, err := mutationAdapterFixture(fs, newMutationAdapterFixtureExecutor()).RestoreStage(context.Background(), expectation); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stringMustRead(t, fs.actual(before.Rendering.ConfdDestinations[0])), "master=192.0.2.10") {
			t.Fatal("confd destination was not restored from run-owned recovery")
		}
	})

	t.Run("OS boot receipt", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 13)
		expectation := rollbackExpectationFixture(t, request, recovery)
		fs.writeFile(t, before.Network.ConfigPath, "DEVICE=eth0\nIPADDR=198.51.100.10\nPREFIX=24\nGATEWAY=192.0.2.1\n", 0o640)
		fake := newMutationAdapterFixtureExecutor()
		fake.bootID = "boot-new"
		receipt, err := mutationAdapterFixture(fs, fake).RestoreStage(context.Background(), expectation)
		if err != nil || !validSHA256Digest(receipt.Digest) || receipt.State != MutationChanged || receipt.BootIDBeforeRollback != "boot-new" {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		for _, command := range fake.commands {
			if command.Name == "reboot" {
				t.Fatalf("rollback adapter rebooted before durable reconnect handoff: %#v", fake.commands)
			}
		}
	})

	t.Run("tampered recovery artifact fails before mutation", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request, recovery := captureRecoveryForStage(t, fs, before, 5)
		expectation := rollbackExpectationFixture(t, request, recovery)
		expectation.Product.KeepalivedConfig.OwnerID = digestBytes([]byte("foreign-owner"))
		fake := newMutationAdapterFixtureExecutor()
		if _, err := mutationAdapterFixture(fs, fake).RestoreStage(context.Background(), expectation); err == nil || len(fake.commands) != 0 {
			t.Fatalf("foreign recovery owner reached mutation: err=%v commands=%#v", err, fake.commands)
		}
	})
}

func TestProductionEtcdRecoveryInspectionUsesCurrentEndpoint(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	fs.writeFile(t, before.Etcd.ConfigPath, "ETCD_ADVERTISE_CLIENT_URLS=http://198.51.100.10:2379\nETCD_INITIAL_ADVERTISE_PEER_URLS=http://198.51.100.10:2380\n", 0o640)
	fake := newMutationAdapterFixtureExecutor()
	fake.peer = "198.51.100.10"
	inspector := &productionReadOnlyInspector{executor: fake, fs: fs}
	contract := RollbackEtcdContract{
		NodeID: "node-master", Role: "master", ConfigPath: before.Etcd.ConfigPath,
		EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme,
		MemberID: before.Etcd.MemberID, MemberCount: 1,
		OldClient: before.Etcd.ClientAddress, ClientPort: before.Etcd.ClientPort,
		OldPeer: before.Etcd.PeerAddress, PeerPort: before.Etcd.PeerPort,
		ServiceName: before.Etcd.ServiceName, ControlAdapter: before.Etcd.ControlAdapter,
	}
	observed, err := inspector.EtcdRecoveryState(context.Background(), contract)
	if err != nil || !observed.Healthy || observed.ClientAddress != "198.51.100.10" || observed.PeerAddress != "198.51.100.10" || observed.MemberID != "1234" || observed.MemberCount != 1 || !validSHA256Digest(observed.LogicalKVDigest) {
		t.Fatalf("observed=%#v err=%v", observed, err)
	}
}

func TestProductionMutationCompositionRemainsPackageLocal(t *testing.T) {
	fake := newMutationAdapterFixtureExecutor()
	base, err := NewDefinition(fake)
	if err != nil {
		t.Fatal(err)
	}
	if base.mutator != nil || base.rollbackMutator != nil || base.inspector != nil || base.rollbackInspector != nil {
		t.Fatal("NewDefinition unexpectedly exposes production mutation adapters")
	}
	definition, err := newProductionDefinitionWithLocalAdapters(fake)
	if err != nil {
		t.Fatal(err)
	}
	if definition.mutator == nil || definition.rollbackMutator == nil || definition.inspector == nil || definition.rollbackInspector == nil {
		t.Fatal("package-local production mutation/rollback composition is incomplete")
	}
}

func TestProductionMutationFailureSurfacesPartialState(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	contract := EtcdStageContract{
		NodeID: "node-master", Role: "master", ConfigPath: before.Etcd.ConfigPath,
		EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme,
		MemberID: before.Etcd.MemberID, MemberCount: 1,
		OldClient: before.Etcd.ClientAddress, NewClient: "198.51.100.10", ClientPort: before.Etcd.ClientPort,
		OldPeer: before.Etcd.PeerAddress, NewPeer: "198.51.100.10", PeerPort: before.Etcd.PeerPort,
		ServiceName: "xetcd", ControlAdapter: "monit",
	}
	fake := newMutationAdapterFixtureExecutor()
	digest, digestErr := deterministicKVDigest([]byte(fake.kvJSON))
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	contract.LogicalKVDigest = digest
	fake.failMatch = "monit restart xetcd"
	receipt, err := mutationAdapterFixture(fs, fake).ReaddressEtcd(context.Background(), contract)
	if err == nil || receipt.State != MutationChanged || !strings.Contains(err.Error(), "partial completion") || !strings.Contains(stringMustRead(t, fs.actual(before.Etcd.ConfigPath)), "198.51.100.10") {
		t.Fatalf("partial etcd failure was not surfaced: receipt=%#v err=%v", receipt, err)
	}
}


func TestProductionMutationLiveDriftStopsBeforeFirstWrite(t *testing.T) {
	aliasContract := AliasStageContract{
		NodeID: "node-master", Role: "master", Interface: "eth0",
		OldAddress: "192.0.2.10", NewAddress: "198.51.100.10", PrefixLength: 24, Gateway: "192.0.2.1",
	}
	t.Run("alias old address lost", func(t *testing.T) {
		fake := newMutationAdapterFixtureExecutor()
		fake.addressJSON = `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.2.99","prefixlen":24,"scope":"global"}]}]`
		receipt, err := mutationAdapterFixture(newMappedRecoveryFilesystem(t), fake).AddTargetAlias(context.Background(), aliasContract)
		if err == nil || receipt.State != MutationNotStarted || mutationCommandSeen(fake.commands, "ip", "address", "add") {
			t.Fatalf("receipt=%#v err=%v commands=%#v", receipt, err, fake.commands)
		}
	})
	t.Run("alias default route drift", func(t *testing.T) {
		fake := newMutationAdapterFixtureExecutor()
		fake.routeJSON = `[{"gateway":"192.0.2.254","dev":"eth0","prefsrc":"192.0.2.10","metric":100}]`
		receipt, err := mutationAdapterFixture(newMappedRecoveryFilesystem(t), fake).AddTargetAlias(context.Background(), aliasContract)
		if err == nil || receipt.State != MutationNotStarted || mutationCommandSeen(fake.commands, "ip", "address", "add") {
			t.Fatalf("receipt=%#v err=%v commands=%#v", receipt, err, fake.commands)
		}
	})

	t.Run("OS boot identity drift", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		original := stringMustRead(t, fs.actual(before.Network.ConfigPath))
		contract := OSStageContract{
			NodeID: "node-master", Role: "master", Interface: "eth0", ConfigPath: before.Network.ConfigPath,
			OldAddress: before.Network.PersistentAddress, NewAddress: "198.51.100.10",
			PrefixLength: before.Network.PersistentPrefix, Gateway: before.Network.PersistentGateway,
			InterfaceAddresses: append([]restoreInterfaceAddress(nil), before.Network.InterfaceAddresses...),
			BootIDBefore: "boot-frozen", Barrier: operation.StageBarrierAgentReconnect, Reboot: true,
		}
		fake := newMutationAdapterFixtureExecutor()
		fake.bootID = "boot-drifted"
		receipt, err := mutationAdapterFixture(fs, fake).CutoverOS(context.Background(), contract)
		if err == nil || receipt.State != MutationNotStarted || stringMustRead(t, fs.actual(before.Network.ConfigPath)) != original {
			t.Fatalf("receipt=%#v err=%v commands=%#v", receipt, err, fake.commands)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*mutationAdapterFixtureExecutor)
	}{
		{name: "unhealthy etcd", mutate: func(fake *mutationAdapterFixtureExecutor) { fake.healthOutput = "endpoint is unhealthy\n" }},
		{name: "extra singleton member", mutate: func(fake *mutationAdapterFixtureExecutor) { fake.memberCount = 2 }},
		{name: "wrong member id", mutate: func(fake *mutationAdapterFixtureExecutor) { fake.memberID = 0x9999 }},
		{name: "logical KV drift", mutate: func(fake *mutationAdapterFixtureExecutor) { fake.kvJSON = kvFixtureJSON([][2]string{{"alpha", "changed"}}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newMappedRecoveryFilesystem(t)
			before := productionBeforeFixture()
			seedProductionFiles(t, fs, before)
			fake := newMutationAdapterFixtureExecutor()
			baselineDigest, err := deterministicKVDigest([]byte(fake.kvJSON))
			if err != nil {
				t.Fatal(err)
			}
			contract := EtcdStageContract{
				NodeID: "node-master", Role: "master", ConfigPath: before.Etcd.ConfigPath,
				EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme,
				MemberID: before.Etcd.MemberID, MemberCount: 1,
				OldClient: before.Etcd.ClientAddress, NewClient: "198.51.100.10", ClientPort: before.Etcd.ClientPort,
				OldPeer: before.Etcd.PeerAddress, NewPeer: "198.51.100.10", PeerPort: before.Etcd.PeerPort,
				ServiceName: before.Etcd.ServiceName, ControlAdapter: before.Etcd.ControlAdapter,
				LogicalKVDigest: baselineDigest,
			}
			tc.mutate(fake)
			receipt, mutationErr := mutationAdapterFixture(fs, fake).ReaddressEtcd(context.Background(), contract)
			if mutationErr == nil || receipt.State != MutationNotStarted || mutationCommandSeen(fake.commands, "env", "member", "update") {
				t.Fatalf("receipt=%#v err=%v commands=%#v", receipt, mutationErr, fake.commands)
			}
		})
	}
}

func TestProductionRollbackPreflightsEveryDestinationBeforeWrite(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	before.Rendering.ConfdDestinations = []string{"/etc/xrocket/a.conf", "/ambiguous/b.conf"}
	seedProductionFiles(t, fs, before)
	fs.writeFile(t, before.Rendering.ConfdDestinations[1], "second=192.0.2.10\n", 0o640)
	request, recovery := captureRecoveryForStage(t, fs, before, 11)
	expectation := rollbackExpectationFixture(t, request, recovery)

	firstMutated := "first=198.51.100.10\n"
	fs.writeFile(t, before.Rendering.ConfdDestinations[0], firstMutated, 0o640)
	if err := os.Remove(fs.actual(before.Rendering.ConfdDestinations[1])); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fs.actual("/ambiguous")); err != nil {
		t.Fatal(err)
	}
	fs.writeFile(t, "/safe/b.conf", "second=198.51.100.10\n", 0o640)
	fs.symlink(t, "/safe", "/ambiguous")

	receipt, err := mutationAdapterFixture(fs, newMutationAdapterFixtureExecutor()).RestoreStage(context.Background(), expectation)
	if err == nil || receipt.State != MutationNotStarted {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if got := stringMustRead(t, fs.actual(before.Rendering.ConfdDestinations[0])); got != firstMutated {
		t.Fatalf("first destination changed before second destination preflight failed: %q", got)
	}
}

func TestStrictEtcdHealthOutputRejectsUnhealthySubstring(t *testing.T) {
	if strictEtcdHealthOutput("endpoint is unhealthy") {
		t.Fatal("unhealthy etcd output was accepted as healthy")
	}
	if !strictEtcdHealthOutput("http://192.0.2.10:2379 is healthy: successfully committed proposal: took = 1ms") {
		t.Fatal("canonical healthy etcd output was rejected")
	}
}

func mutationCommandSeen(commands []executor.Command, name string, fragments ...string) bool {
	for _, command := range commands {
		if command.Name != name {
			continue
		}
		matched := true
		for _, fragment := range fragments {
			found := false
			for _, argument := range command.Args {
				if argument == fragment {
					found = true
					break
				}
			}
			if !found {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
