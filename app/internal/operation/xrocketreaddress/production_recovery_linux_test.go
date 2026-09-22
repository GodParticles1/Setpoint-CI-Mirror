//go:build linux

package xrocketreaddress

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"setpoint/internal/executor"
)

type mappedRecoveryFilesystem struct {
	root string
}

func newMappedRecoveryFilesystem(t *testing.T) *mappedRecoveryFilesystem {
	t.Helper()
	return &mappedRecoveryFilesystem{root: t.TempDir()}
}

func (fs *mappedRecoveryFilesystem) actual(name string) string {
	return filepath.Join(fs.root, filepath.FromSlash(strings.TrimPrefix(name, "/")))
}

func (fs *mappedRecoveryFilesystem) logical(name string) string {
	relative, err := filepath.Rel(fs.root, name)
	if err != nil || strings.HasPrefix(relative, "..") {
		return name
	}
	return "/" + filepath.ToSlash(relative)
}

func (fs *mappedRecoveryFilesystem) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(fs.actual(name))
}
func (fs *mappedRecoveryFilesystem) EvalSymlinks(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(fs.actual(name))
	if err != nil {
		return "", err
	}
	return fs.logical(resolved), nil
}
func (fs *mappedRecoveryFilesystem) Open(name string) (*os.File, error) {
	return os.Open(fs.actual(name))
}
func (fs *mappedRecoveryFilesystem) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(fs.actual(name), flag, perm)
}
func (fs *mappedRecoveryFilesystem) MkdirAll(name string, perm os.FileMode) error {
	return os.MkdirAll(fs.actual(name), perm)
}
func (fs *mappedRecoveryFilesystem) Mkdir(name string, perm os.FileMode) error {
	return os.Mkdir(fs.actual(name), perm)
}
func (fs *mappedRecoveryFilesystem) Chmod(name string, mode os.FileMode) error {
	return os.Chmod(fs.actual(name), mode)
}
func (fs *mappedRecoveryFilesystem) Rename(oldPath, newPath string) error {
	return os.Rename(fs.actual(oldPath), fs.actual(newPath))
}
func (fs *mappedRecoveryFilesystem) Remove(name string) error { return os.Remove(fs.actual(name)) }
func (fs *mappedRecoveryFilesystem) RemoveAll(name string) error {
	return os.RemoveAll(fs.actual(name))
}

func (fs *mappedRecoveryFilesystem) writeFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	actual := fs.actual(name)
	if err := os.MkdirAll(filepath.Dir(actual), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func (fs *mappedRecoveryFilesystem) symlink(t *testing.T, target, name string) {
	t.Helper()
	actual := fs.actual(name)
	if err := os.MkdirAll(filepath.Dir(actual), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fs.actual(target), actual); err != nil {
		t.Fatal(err)
	}
}

type productionFixtureExecutor struct {
	fs                      *mappedRecoveryFilesystem
	commands                []executor.Command
	memberID                uint64
	memberCount             int
	healthy                 bool
	kvJSON                  string
	kvJSONAfter             string
	kvCalls                 int
	snapshotBytes           string
	snapshotSize            int64
	snapshotStdoutTruncated bool
	snapshotStderrTruncated bool
	bootID                  string
	addressJSON             string
	routeJSON               string
	keepalivedText          string
}

func newProductionFixtureExecutor(fs *mappedRecoveryFilesystem) *productionFixtureExecutor {
	return &productionFixtureExecutor{
		fs:            fs,
		memberID:      0x1234,
		memberCount:   1,
		healthy:       true,
		kvJSON:        kvFixtureJSON([][2]string{{"alpha", "secret-one"}, {"beta", "secret-two"}}),
		snapshotBytes: "bounded-etcd-snapshot",
		bootID:        "boot-production-readonly",
		addressJSON:   `[{"ifname":"eth0","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24,"scope":"global"},{"family":"inet","local":"198.51.100.10","prefixlen":24,"scope":"global"}]}]`,
		routeJSON:     `[{"gateway":"192.0.2.1","dev":"eth0","prefsrc":"192.0.2.10","metric":100}]`,
		keepalivedText: `vrrp_instance VI_1 {
 state BACKUP
 interface eth0
 priority 100
 nopreempt
 unicast_src_ip 192.0.2.10
 unicast_peer { 192.0.2.11 }
 virtual_ipaddress { 192.0.2.12/24 dev eth0 }
}
virtual_server 192.0.2.12 6000 { delay_loop 3 }
`,
	}
}

func (fake *productionFixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	fake.commands = append(fake.commands, command)
	switch {
	case command.Name == "env" && len(command.Args) == 5 && command.Args[3] == "endpoint" && command.Args[4] == "health":
		if !fake.healthy {
			return executor.Result{ExitCode: 1}, &executor.Error{Kind: executor.ErrorExit, Result: executor.Result{ExitCode: 1}, Err: errors.New("unhealthy")}
		}
		return executor.Result{Stdout: "endpoint is healthy\n"}, nil
	case command.Name == "env" && len(command.Args) == 7 && command.Args[3] == "member" && command.Args[4] == "list":
		members := make([]map[string]any, 0, fake.memberCount)
		for index := 0; index < fake.memberCount; index++ {
			id := fake.memberID + uint64(index)
			members = append(members, map[string]any{"ID": id, "peerURLs": []string{"http://192.0.2.10:2380"}})
		}
		payload, _ := json.Marshal(map[string]any{"members": members})
		return executor.Result{Stdout: string(payload)}, nil
	case command.Name == "env" && len(command.Args) == 8 && command.Args[3] == "get" && command.Args[5] == "--prefix":
		fake.kvCalls++
		payload := fake.kvJSON
		if fake.kvCalls > 1 && fake.kvJSONAfter != "" {
			payload = fake.kvJSONAfter
		}
		return executor.Result{Stdout: payload}, nil
	case command.Name == "env" && len(command.Args) == 6 && command.Args[3] == "snapshot" && command.Args[4] == "save":
		if fake.fs == nil {
			return executor.Result{}, errors.New("snapshot fixture filesystem is missing")
		}
		fake.fs.writeFileNoTest(command.Args[5], fake.snapshotBytes, 0o600)
		if fake.snapshotSize > 0 {
			if err := os.Truncate(fake.fs.actual(command.Args[5]), fake.snapshotSize); err != nil {
				return executor.Result{}, err
			}
		}
		return executor.Result{Stdout: "snapshot saved\n", StdoutTruncated: fake.snapshotStdoutTruncated, StderrTruncated: fake.snapshotStderrTruncated}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/proc/sys/kernel/random/boot_id"}):
		return executor.Result{Stdout: fake.bootID + "\n"}, nil
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "address", "show"}):
		return executor.Result{Stdout: fake.addressJSON}, nil
	case command.Name == "ip" && reflect.DeepEqual(command.Args, []string{"-j", "route", "show", "default"}):
		return executor.Result{Stdout: fake.routeJSON}, nil
	case command.Name == "printenv" && reflect.DeepEqual(command.Args, []string{"HOME"}):
		return executor.Result{Stdout: "/root\n"}, nil
	case command.Name == "test" && len(command.Args) == 2 && command.Args[0] == "-e" && command.Args[1] == "/etc/keepalived/keepalived.conf":
		return executor.Result{ExitCode: 0}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/keepalived/keepalived.conf"}):
		return executor.Result{Stdout: fake.keepalivedText}, nil
	default:
		return executor.Result{}, fmt.Errorf("unexpected production fixture command: %s %s", command.Name, strings.Join(command.Args, " "))
	}
}

func (fs *mappedRecoveryFilesystem) writeFileNoTest(name, content string, mode os.FileMode) {
	actual := fs.actual(name)
	_ = os.MkdirAll(filepath.Dir(actual), 0o755)
	_ = os.WriteFile(actual, []byte(content), mode)
}

func kvFixtureJSON(values [][2]string) string {
	items := make([]map[string]string, 0, len(values))
	for _, value := range values {
		items = append(items, map[string]string{
			"key":   base64.StdEncoding.EncodeToString([]byte(value[0])),
			"value": base64.StdEncoding.EncodeToString([]byte(value[1])),
		})
	}
	payload, _ := json.Marshal(map[string]any{"kvs": items})
	return string(payload)
}

func productionBeforeFixture() restoreBeforeState {
	return restoreBeforeState{
		Site: restoreSiteState{MasterAddress: "192.0.2.10", SlaveAddress: "192.0.2.11", VIPAddress: "192.0.2.12"},
		Network: restoreNetworkState{
			Address: "192.0.2.10", PrefixLength: 24, Interface: "eth0",
			InterfaceAddresses: []restoreInterfaceAddress{{Address: "192.0.2.10", PrefixLength: 24}},
			Gateway:            "192.0.2.1", Backend: "ifcfg", ConfigPath: "/etc/sysconfig/network-scripts/ifcfg-eth0",
			PersistentAddress: "192.0.2.10", PersistentPrefix: 24, PersistentGateway: "192.0.2.1",
		},
		Product: restoreProductState{
			Generation: "V300R004C68B009", VersionEvidence: "/opt/data/V300R004C68B009/xrocket",
			InstallProfile: installProfileRootNative, OSAuthorityUID: 0, ProductUser: "root", ProductHome: "/root", ProductPrefix: "",
			XrocketBinary: "/opt/data/xrocket/xrocket.v2", XrocketVersion: "0.15.458", CommonYAMLPath: "/etc/confd/common.yaml",
		},
		Database: restoreDatabaseState{Address: "203.0.113.80", Port: 8888},
		Etcd: restoreEtcdState{
			ConfigPath: "/etc/etcd/etcd.conf", EtcdctlPath: "/opt/etcd/etcdctl", Scheme: "http",
			ClientAddress: "192.0.2.10", ClientPort: 2379, PeerAddress: "192.0.2.10", PeerPort: 2380,
			MemberID: "1234", MemberCount: 1, ServiceName: "xetcd", ControlAdapter: "monit",
		},
		HA: restoreHAState{
			ConfigPath: "/etc/keepalived/keepalived.conf", ConfiguredRole: "BACKUP", RuntimeRole: "active_vip_owner", Interface: "eth0",
			SourceAddress: "192.0.2.10", PeerAddress: "192.0.2.11", VIPAddress: "192.0.2.12", Priority: 100, Nopreempt: true, BusinessPorts: []int{6000},
		},
		Rendering: restoreRenderingState{BootID: "boot-before", ConfdDestinations: []string{"/etc/xrocket/rendered.conf"}},
	}
}

func productionCaptureRequest(t *testing.T, stageIndex int, before restoreBeforeState) RecoveryArtifactCaptureRequest {
	t.Helper()
	spec := canonicalStages[stageIndex]
	nodeID := "node-master"
	manifest := restorePointManifest{
		RunID: "run-production-recovery", StageID: spec.ID, StageIndex: stageIndex, NodeID: nodeID,
		ParticipantNodeIDs: []string{"node-master", "node-slave"}, Role: spec.Role,
	}
	return RecoveryArtifactCaptureRequest{Owner: recoveryOwnerFromManifest(manifest), StageKind: string(spec.Kind), Before: before}
}

func seedProductionFiles(t *testing.T, fs *mappedRecoveryFilesystem, before restoreBeforeState) {
	t.Helper()
	files := map[string]string{
		before.Network.ConfigPath:     "DEVICE=eth0\nIPADDR=192.0.2.10\nPREFIX=24\nGATEWAY=192.0.2.1\n",
		before.Product.CommonYAMLPath: "goldendb:\n  address: 203.0.113.80\n  port: 8888\n  username: app\n  password: do-not-log\n  database: xrocket\n  schema: app_schema\n",
		before.Etcd.ConfigPath:        "ETCD_ADVERTISE_CLIENT_URLS=http://192.0.2.10:2379\nETCD_INITIAL_ADVERTISE_PEER_URLS=http://192.0.2.10:2380\n",
		before.HA.ConfigPath: `vrrp_instance VI_1 {
 state BACKUP
 interface eth0
 priority 100
 nopreempt
 unicast_src_ip 192.0.2.10
 unicast_peer { 192.0.2.11 }
 virtual_ipaddress { 192.0.2.12/24 dev eth0 }
}
virtual_server 192.0.2.12 6000 { delay_loop 3 }
`,
		before.Rendering.ConfdDestinations[0]:                               "master=192.0.2.10 slave=192.0.2.11 vip=192.0.2.12 db=203.0.113.80 mode=stable\n",
		pathJoin(before.Product.VersionEvidence, "package/nodes.yaml"):      "master: 192.0.2.10\nslave: 192.0.2.11\nvip: 192.0.2.12\nmode: dual\n",
		pathJoin(before.Product.VersionEvidence, "package/etcd.yaml"):       "master: 192.0.2.10\nslave: 192.0.2.11\n",
		pathJoin(before.Product.VersionEvidence, "package/solution.config"): "vip=192.0.2.12\n",
		"/etc/hosts":            "127.0.0.1 localhost\n192.0.2.10 master\n192.0.2.11 slave\n",
		"/etc/nats/simple.conf": "routes=192.0.2.10,192.0.2.11\nvip=192.0.2.12\n",
	}
	for name, content := range files {
		fs.writeFile(t, name, content, 0o640)
	}
}

func pathJoin(root, suffix string) string { return strings.TrimSuffix(root, "/") + "/" + suffix }

func TestProductionRecoveryOwnerPermissionsAndCanonicalArtifact(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	fake := newProductionFixtureExecutor(fs)
	collector := &productionRecoveryCollector{executor: fake, fs: fs}
	request := productionCaptureRequest(t, 3, before)
	contract, err := collector.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != RecoveryArtifactContractSchema || !reflect.DeepEqual(contract.Owner, request.Owner) || len(contract.Artifacts) != 1 {
		t.Fatalf("contract=%#v", contract)
	}
	artifact := contract.Artifacts[0]
	if artifact.Kind != recoveryKindNetworkConfig || artifact.SourcePath != before.Network.ConfigPath || !strings.HasPrefix(artifact.BackupRef, recoveryBackupRoot(request.Owner.OwnerID)+"/") {
		t.Fatalf("artifact=%#v", artifact)
	}
	rootInfo, err := os.Stat(fs.actual(recoveryBackupRoot(request.Owner.OwnerID)))
	if err != nil || rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("owner root mode=%v err=%v", rootInfo.Mode().Perm(), err)
	}
	artifactInfo, err := os.Stat(fs.actual(artifact.BackupRef))
	if err != nil || artifactInfo.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%v err=%v", artifactInfo.Mode().Perm(), err)
	}
	if _, err := os.Stat(fs.actual(artifact.BackupRef + ".tmp")); !os.IsNotExist(err) {
		t.Fatalf("partial temp artifact survived atomic capture: %v", err)
	}
}

func TestProductionRecoveryRejectsOwnerPathAndSymlinkConfusion(t *testing.T) {
	t.Run("owner correlation", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		request := productionCaptureRequest(t, 3, before)
		request.Owner.RunID = "foreign-run"
		collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
		if _, err := collector.Capture(context.Background(), request); err == nil {
			t.Fatal("foreign recovery owner was accepted")
		}
	})
	t.Run("path traversal", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		before.Network.ConfigPath = "/etc/sysconfig/../escape"
		seedProductionFiles(t, fs, before)
		request := productionCaptureRequest(t, 3, before)
		collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
		if _, err := collector.Capture(context.Background(), request); err == nil {
			t.Fatal("path traversal source was accepted")
		}
	})
	t.Run("symlink source", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		if err := os.Remove(fs.actual(before.Network.ConfigPath)); err != nil {
			t.Fatal(err)
		}
		fs.writeFile(t, "/safe/network", "safe\n", 0o600)
		fs.symlink(t, "/safe/network", before.Network.ConfigPath)
		request := productionCaptureRequest(t, 3, before)
		collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
		if _, err := collector.Capture(context.Background(), request); err == nil {
			t.Fatal("symlink recovery source was accepted")
		}
		if _, err := os.Stat(fs.actual(recoveryBackupRoot(request.Owner.OwnerID))); !os.IsNotExist(err) {
			t.Fatalf("failed capture retained owner root: %v", err)
		}
	})
}

func TestProductionRecoveryPartialCaptureAndProductBundleBoundary(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	missing := pathJoin(before.Product.VersionEvidence, "package/etcd.yaml")
	if err := os.Remove(fs.actual(missing)); err != nil {
		t.Fatal(err)
	}
	request := productionCaptureRequest(t, 5, before)
	collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
	if _, err := collector.Capture(context.Background(), request); err == nil {
		t.Fatal("partial product recovery capture returned a valid contract")
	}
	if _, err := os.Stat(fs.actual(recoveryBackupRoot(request.Owner.OwnerID))); !os.IsNotExist(err) {
		t.Fatalf("partial product capture retained owner root: %v", err)
	}

	fs = newMappedRecoveryFilesystem(t)
	seedProductionFiles(t, fs, before)
	request = productionCaptureRequest(t, 5, before)
	collector = &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
	contract, err := collector.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := recoveryArtifactByKindSource(contract, recoveryKindProductBundle, before.Product.VersionEvidence)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readRecoveryFileArtifact(fs, bundle.BackupRef)
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, entry := range decoded.Entries {
		sources = append(sources, entry.SourcePath)
	}
	expected := controlledProductRecoveryPaths(before)
	if !reflect.DeepEqual(sources, expected) {
		t.Fatalf("product bundle sources=%v want=%v", sources, expected)
	}
}

func TestProductionRecoveryEtcdSnapshotIdentityAndSecretFreeKVDigest(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	fake := newProductionFixtureExecutor(fs)
	collector := &productionRecoveryCollector{executor: fake, fs: fs}
	request := productionCaptureRequest(t, 9, before)
	contract, err := collector.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(contract.Artifacts) != 2 || !validSHA256Digest(contract.LogicalKVDigest) {
		t.Fatalf("etcd recovery contract=%#v", contract)
	}
	snapshot, err := recoveryArtifactByKindSource(contract, recoveryKindEtcdSnapshot, before.Etcd.EtcdctlPath)
	if err != nil {
		t.Fatal(err)
	}
	if stringMustRead(t, fs.actual(snapshot.BackupRef)) != fake.snapshotBytes {
		t.Fatal("snapshot persisted bytes differ from etcdctl output")
	}
	actualDigest, err := hashFile(fs, snapshot.BackupRef)
	if err != nil || actualDigest != snapshot.SHA256 {
		t.Fatalf("snapshot digest=%s want=%s err=%v", actualDigest, snapshot.SHA256, err)
	}
	foundSnapshot := false
	for _, command := range fake.commands {
		if command.Name == "env" && len(command.Args) == 6 && command.Args[3] == "snapshot" && command.Args[4] == "save" {
			foundSnapshot = command.Args[1] == before.Etcd.EtcdctlPath && strings.HasPrefix(command.Args[5], recoveryBackupRoot(request.Owner.OwnerID)+"/") && command.OutputLimit == maxEtcdSnapshotCommandOutputBytes
		}
	}
	if !foundSnapshot {
		t.Fatal("etcd snapshot did not use the frozen etcdctl identity and run-owned output")
	}
	first, err := deterministicKVDigest([]byte(kvFixtureJSON([][2]string{{"alpha", "secret-one"}, {"beta", "secret-two"}})))
	if err != nil {
		t.Fatal(err)
	}
	second, err := deterministicKVDigest([]byte(kvFixtureJSON([][2]string{{"beta", "secret-two"}, {"alpha", "secret-one"}})))
	if err != nil || first != second || first != contract.LogicalKVDigest {
		t.Fatalf("deterministic KV digest first=%s second=%s contract=%s err=%v", first, second, contract.LogicalKVDigest, err)
	}
	encoded, _ := json.Marshal(contract)
	if strings.Contains(string(encoded), "secret-one") || strings.Contains(string(encoded), "secret-two") || strings.Contains(string(encoded), base64.StdEncoding.EncodeToString([]byte("secret-one"))) {
		t.Fatalf("raw KV material leaked into contract: %s", encoded)
	}
}

func TestProductionRecoveryEtcdSnapshotStabilityAndBounds(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*productionFixtureExecutor)
	}{
		{name: "kv-drift-window", configure: func(fake *productionFixtureExecutor) {
			fake.kvJSONAfter = kvFixtureJSON([][2]string{{"alpha", "changed"}})
		}},
		{name: "snapshot-oversize", configure: func(fake *productionFixtureExecutor) { fake.snapshotSize = maxEtcdSnapshotBytes + 1 }},
		{name: "snapshot-stdout-truncated", configure: func(fake *productionFixtureExecutor) { fake.snapshotStdoutTruncated = true }},
		{name: "snapshot-stderr-truncated", configure: func(fake *productionFixtureExecutor) { fake.snapshotStderrTruncated = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newMappedRecoveryFilesystem(t)
			before := productionBeforeFixture()
			seedProductionFiles(t, fs, before)
			fake := newProductionFixtureExecutor(fs)
			tc.configure(fake)
			collector := &productionRecoveryCollector{executor: fake, fs: fs}
			request := productionCaptureRequest(t, 9, before)
			if _, err := collector.Capture(context.Background(), request); err == nil {
				t.Fatalf("%s returned a valid recovery contract", tc.name)
			}
			if _, err := os.Stat(fs.actual(recoveryBackupRoot(request.Owner.OwnerID))); !os.IsNotExist(err) {
				t.Fatalf("%s retained owner recovery root: %v", tc.name, err)
			}
		})
	}
}

func TestProductionRecoveryEtcdIdentityMismatchFailsBeforeSnapshot(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	fake := newProductionFixtureExecutor(fs)
	fake.memberID = 0x9999
	collector := &productionRecoveryCollector{executor: fake, fs: fs}
	request := productionCaptureRequest(t, 9, before)
	if _, err := collector.Capture(context.Background(), request); err == nil {
		t.Fatal("etcd member identity mismatch was accepted")
	}
	for _, command := range fake.commands {
		if command.Name == "env" && len(command.Args) >= 4 && command.Args[3] == "snapshot" {
			t.Fatal("etcd snapshot ran after identity mismatch")
		}
	}
	if _, err := os.Stat(fs.actual(recoveryBackupRoot(request.Owner.OwnerID))); !os.IsNotExist(err) {
		t.Fatalf("failed etcd capture retained owner root: %v", err)
	}
}

func TestProductionInspectorTamperBootAndWrongPostcondition(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	fake := newProductionFixtureExecutor(fs)
	collector := &productionRecoveryCollector{executor: fake, fs: fs}
	request := productionCaptureRequest(t, 3, before)
	contract, err := collector.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	inspector := &productionReadOnlyInspector{executor: fake, probe: discoveryProbe{executor: fake}, fs: fs, dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not used") }}
	matched, err := inspector.RecoveryArtifactMatches(context.Background(), contract.Artifacts[0])
	if err != nil || !matched {
		t.Fatalf("original backup match=%v err=%v", matched, err)
	}
	originalDigest := contract.Artifacts[0].SHA256
	if err := os.WriteFile(fs.actual(contract.Artifacts[0].BackupRef), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	newDigest, err := hashFile(fs, contract.Artifacts[0].BackupRef)
	if err != nil || newDigest == originalDigest {
		t.Fatalf("persisted-byte tamper did not change SHA: old=%s new=%s err=%v", originalDigest, newDigest, err)
	}
	matched, err = inspector.RecoveryArtifactMatches(context.Background(), contract.Artifacts[0])
	if err != nil || matched {
		t.Fatalf("tampered backup match=%v err=%v", matched, err)
	}
	bootID, err := inspector.CurrentBootID(context.Background(), RollbackOSContract{})
	if err != nil || bootID != fake.bootID {
		t.Fatalf("boot_id=%q err=%v", bootID, err)
	}
	wrong := AliasStageContract{NodeID: "node-master", Role: "master", Interface: "eth0", OldAddress: "192.0.2.10", NewAddress: "198.51.100.99", PrefixLength: 24, Gateway: "192.0.2.1"}
	satisfied, err := inspector.AliasSatisfied(context.Background(), wrong)
	if err != nil || satisfied {
		t.Fatalf("wrong observed postcondition satisfied=%v err=%v", satisfied, err)
	}
}

func TestProductionReadOnlyInspectorExternalDBBaselineRejectsNonAddressDrift(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
	request := productionCaptureRequest(t, 7, before)
	recovery, err := collector.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := recoveryArtifactByKindSource(recovery, recoveryKindCommonYAML, before.Product.CommonYAMLPath)
	if err != nil {
		t.Fatal(err)
	}
	fake := newProductionFixtureExecutor(fs)
	inspector := &productionReadOnlyInspector{executor: fake, probe: discoveryProbe{executor: fake}, fs: fs, dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not used") }}
	contract := ExternalDBStageContract{NodeID: "node-master", Role: "master", CommonYAMLPath: before.Product.CommonYAMLPath, OldAddress: before.Database.Address, NewAddress: "203.0.113.20", Port: 8888, PreserveNonAddressFields: true, BaselineConfig: baseline}
	positive := "goldendb:\n  address: 203.0.113.20\n  port: 8888\n  username: app\n  password: do-not-log\n  database: xrocket\n  schema: app_schema\n"
	fs.writeFile(t, before.Product.CommonYAMLPath, positive, 0o640)
	if satisfied, err := inspector.ExternalDBSatisfied(context.Background(), contract); err != nil || !satisfied {
		t.Fatalf("exact address-only transition satisfied=%v err=%v", satisfied, err)
	}
	negative := map[string]string{
		"password":           "goldendb:\n  address: 203.0.113.20\n  port: 8888\n  username: app\n  password: changed\n  database: xrocket\n  schema: app_schema\n",
		"username":           "goldendb:\n  address: 203.0.113.20\n  port: 8888\n  username: other\n  password: do-not-log\n  database: xrocket\n  schema: app_schema\n",
		"database":           "goldendb:\n  address: 203.0.113.20\n  port: 8888\n  username: app\n  password: do-not-log\n  database: other\n  schema: app_schema\n",
		"schema":             "goldendb:\n  address: 203.0.113.20\n  port: 8888\n  username: app\n  password: do-not-log\n  database: xrocket\n  schema: other\n",
		"port":               "goldendb:\n  address: 203.0.113.20\n  port: 9999\n  username: app\n  password: do-not-log\n  database: xrocket\n  schema: app_schema\n",
		"duplicate-endpoint": "goldendb:\n  address: 203.0.113.20\n  host: 203.0.113.20\n  port: 8888\n  username: app\n  password: do-not-log\n  database: xrocket\n  schema: app_schema\n",
	}
	for name, content := range negative {
		t.Run(name, func(t *testing.T) {
			fs.writeFile(t, before.Product.CommonYAMLPath, content, 0o640)
			satisfied, err := inspector.ExternalDBSatisfied(context.Background(), contract)
			if err == nil && satisfied {
				t.Fatalf("drift %s produced false positive", name)
			}
		})
	}
	encoded, _ := json.Marshal(contract)
	if strings.Contains(string(encoded), "do-not-log") || strings.Contains(string(encoded), "username") || strings.Contains(string(encoded), "database: xrocket") {
		t.Fatalf("raw common.yaml secret/config content leaked into contract: %s", encoded)
	}
}

func TestProductionReadOnlyInspectorProductAndConfdRejectFalsePositiveDrift(t *testing.T) {
	t.Run("product", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
		request := productionCaptureRequest(t, 5, before)
		recovery, err := collector.Capture(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		bundle, _ := recoveryArtifactByKindSource(recovery, recoveryKindProductBundle, before.Product.VersionEvidence)
		keepalived, _ := recoveryArtifactByKindSource(recovery, recoveryKindKeepalivedConfig, before.HA.ConfigPath)
		contract := ProductStageContract{NodeID: "node-master", Role: "master", InstallProfile: before.Product.InstallProfile, ProductUser: before.Product.ProductUser, ProductHome: before.Product.ProductHome, ProductPrefix: before.Product.ProductPrefix, XrocketBinary: before.Product.XrocketBinary, OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, NewMaster: "198.51.100.10", NewSlave: "198.51.100.11", NewVIP: "198.51.100.12", ProductBundle: bundle, KeepalivedConfig: keepalived}
		writePositiveProductTransition(t, fs, before)
		fake := newProductionFixtureExecutor(fs)
		inspector := &productionReadOnlyInspector{executor: fake, probe: discoveryProbe{executor: fake}, fs: fs, dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not used") }}
		if satisfied, err := inspector.ProductSatisfied(context.Background(), contract); err != nil || !satisfied {
			t.Fatalf("positive product transition satisfied=%v err=%v", satisfied, err)
		}
		cases := map[string]string{
			"comment-only-new": "master: 192.0.2.10\nslave: 198.51.100.11\nvip: 198.51.100.12\nmode: dual\n# target 198.51.100.10\n",
			"old-new-coexist":  "master: 198.51.100.10\nmaster_backup: 192.0.2.10\nslave: 198.51.100.11\nvip: 198.51.100.12\nmode: dual\n",
			"non-ip-drift":     "master: 198.51.100.10\nslave: 198.51.100.11\nvip: 198.51.100.12\nmode: changed\n",
		}
		for name, content := range cases {
			t.Run(name, func(t *testing.T) {
				writePositiveProductTransition(t, fs, before)
				fs.writeFile(t, pathJoin(before.Product.VersionEvidence, "package/nodes.yaml"), content, 0o640)
				satisfied, err := inspector.ProductSatisfied(context.Background(), contract)
				if err == nil && satisfied {
					t.Fatalf("product false positive %s", name)
				}
			})
		}
	})

	t.Run("confd", func(t *testing.T) {
		fs := newMappedRecoveryFilesystem(t)
		before := productionBeforeFixture()
		seedProductionFiles(t, fs, before)
		collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
		request := productionCaptureRequest(t, 11, before)
		recovery, err := collector.Capture(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		contract := ConfdStageContract{NodeID: "node-master", Role: "master", InstallProfile: before.Product.InstallProfile, ProductUser: before.Product.ProductUser, ProductHome: before.Product.ProductHome, ProductPrefix: before.Product.ProductPrefix, XrocketBinary: before.Product.XrocketBinary, Destinations: append([]string(nil), before.Rendering.ConfdDestinations...), DestinationFiles: append([]RecoveryArtifactRef(nil), recovery.Artifacts...), OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, OldExternalDB: before.Database.Address, ExpectedMaster: "198.51.100.10", ExpectedSlave: "198.51.100.11", ExpectedVIP: "198.51.100.12", ExpectedExternalDB: "203.0.113.20"}
		fake := newProductionFixtureExecutor(fs)
		inspector := &productionReadOnlyInspector{executor: fake, probe: discoveryProbe{executor: fake}, fs: fs, dial: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("not used") }}
		positive := "master=198.51.100.10 slave=198.51.100.11 vip=198.51.100.12 db=203.0.113.20 mode=stable\n"
		fs.writeFile(t, before.Rendering.ConfdDestinations[0], positive, 0o640)
		if satisfied, err := inspector.ConfdSatisfied(context.Background(), contract); err != nil || !satisfied {
			t.Fatalf("positive confd transition satisfied=%v err=%v", satisfied, err)
		}
		cases := map[string]string{
			"comment-only-new": "master=192.0.2.10 slave=198.51.100.11 vip=198.51.100.12 db=203.0.113.20 mode=stable # target=198.51.100.10\n",
			"old-new-coexist":  "master=198.51.100.10 old_master=192.0.2.10 slave=198.51.100.11 vip=198.51.100.12 db=203.0.113.20 mode=stable\n",
			"non-ip-drift":     "master=198.51.100.10 slave=198.51.100.11 vip=198.51.100.12 db=203.0.113.20 mode=changed\n",
		}
		for name, content := range cases {
			t.Run(name, func(t *testing.T) {
				fs.writeFile(t, before.Rendering.ConfdDestinations[0], content, 0o640)
				satisfied, err := inspector.ConfdSatisfied(context.Background(), contract)
				if err == nil && satisfied {
					t.Fatalf("confd false positive %s", name)
				}
			})
		}
	})
}

func TestProductionV2ExpectationBindsRecoveryBaselinesWithoutRawSecrets(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	collector := &productionRecoveryCollector{executor: newProductionFixtureExecutor(fs), fs: fs}
	plan := executionPlan{Parameters: parameters{MasterTargetAddress: "198.51.100.10", SlaveTargetAddress: "198.51.100.11", VIPTargetAddress: "198.51.100.12", ExternalDBTargetAddress: "203.0.113.20"}}
	for _, stageIndex := range []int{5, 7, 11} {
		request := productionCaptureRequest(t, stageIndex, before)
		recovery, err := collector.Capture(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		manifest := restorePointManifest{SchemaVersion: RestorePointRollbackSchema, RunID: request.Owner.RunID, StageID: request.Owner.StageID, StageIndex: request.Owner.StageIndex, NodeID: request.Owner.NodeID, ParticipantNodeIDs: append([]string(nil), request.Owner.ParticipantNodeIDs...), Role: request.Owner.Role, Before: before, Recovery: &recovery}
		expectation, err := buildStageExpectation(plan, manifest, canonicalStages[stageIndex])
		if err != nil {
			t.Fatal(err)
		}
		switch canonicalStages[stageIndex].Kind {
		case stageKindProduct:
			if expectation.Product == nil || !hasRecoveryArtifactRef(expectation.Product.ProductBundle) || !hasRecoveryArtifactRef(expectation.Product.KeepalivedConfig) {
				t.Fatalf("product v2 baselines not bound: %#v", expectation.Product)
			}
		case stageKindExternalDB:
			if expectation.ExternalDB == nil || !hasRecoveryArtifactRef(expectation.ExternalDB.BaselineConfig) {
				t.Fatalf("external DB v2 baseline not bound: %#v", expectation.ExternalDB)
			}
		case stageKindConfd:
			if expectation.Confd == nil || len(expectation.Confd.DestinationFiles) != len(before.Rendering.ConfdDestinations) {
				t.Fatalf("confd v2 baselines not bound: %#v", expectation.Confd)
			}
		}
		encoded, _ := json.Marshal(expectation)
		if strings.Contains(string(encoded), "do-not-log") || strings.Contains(string(encoded), "secret-one") {
			t.Fatalf("raw secret leaked into v2 stage expectation: %s", encoded)
		}
		if err := os.RemoveAll(fs.actual(recoveryBackupRoot(request.Owner.OwnerID))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProductionReadOnlyHomePrefixDoesNotGainRecoveryAuthority(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	fake := newProductionFixtureExecutor(fs)
	before.Product.InstallProfile = installProfileHomePrefix
	before.Product.ProductUser = "xbrother"
	before.Product.ProductHome = "/home/xbrother"
	before.Product.ProductPrefix = "/home/xbrother"
	before.Product.XrocketBinary = "/home/xbrother/opt/data/xrocket/xrocket.v2"
	before.Product.CommonYAMLPath = "/home/xbrother/etc/confd/common.yaml"
	request := productionCaptureRequest(t, 3, before)
	collector := &productionRecoveryCollector{executor: fake, fs: fs}
	if _, err := collector.Capture(context.Background(), request); err == nil || !strings.Contains(err.Error(), "root-native") {
		t.Fatalf("HOME-prefixed product gained recovery/mutation authority: %v", err)
	}
}

func writePositiveProductTransition(t *testing.T, fs *mappedRecoveryFilesystem, before restoreBeforeState) {
	t.Helper()
	files := map[string]string{
		pathJoin(before.Product.VersionEvidence, "package/nodes.yaml"):      "master: 198.51.100.10\nslave: 198.51.100.11\nvip: 198.51.100.12\nmode: dual\n",
		pathJoin(before.Product.VersionEvidence, "package/etcd.yaml"):       "master: 198.51.100.10\nslave: 198.51.100.11\n",
		pathJoin(before.Product.VersionEvidence, "package/solution.config"): "vip=198.51.100.12\n",
		"/etc/hosts":            "127.0.0.1 localhost\n198.51.100.10 master\n198.51.100.11 slave\n",
		"/etc/nats/simple.conf": "routes=198.51.100.10,198.51.100.11\nvip=198.51.100.12\n",
		before.HA.ConfigPath: `vrrp_instance VI_1 {
 state BACKUP
 interface eth0
 priority 100
 nopreempt
 unicast_src_ip 198.51.100.10
 unicast_peer { 198.51.100.11 }
 virtual_ipaddress { 198.51.100.12/24 dev eth0 }
}
virtual_server 198.51.100.12 6000 { delay_loop 3 }
`,
	}
	for name, content := range files {
		fs.writeFile(t, name, content, 0o640)
	}
}

func TestProductionFactoryComposesRecoveryAndReadOnlyOnly(t *testing.T) {
	fake := newProductionFixtureExecutor(nil)
	provider, err := NewProductionRestorePointProvider(fake)
	if err != nil {
		t.Fatal(err)
	}
	concrete, ok := provider.(*restorePointProvider)
	if !ok || concrete.recoveryCollector == nil || concrete.rollbackInspector == nil {
		t.Fatalf("production restore provider=%#v", provider)
	}
	definition, err := NewDefinition(fake)
	if err != nil {
		t.Fatal(err)
	}
	if definition.mutator != nil || definition.rollbackMutator != nil || definition.inspector != nil || definition.rollbackInspector != nil {
		t.Fatal("production NewDefinition unexpectedly wired mutation/read-only execution adapters")
	}
}

func stringMustRead(t *testing.T, name string) string {
	t.Helper()
	value, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestControlledProductRecoveryPathsAreStable(t *testing.T) {
	before := productionBeforeFixture()
	actual := controlledProductRecoveryPaths(before)
	expected := []string{
		"/opt/data/V300R004C68B009/xrocket/package/nodes.yaml",
		"/opt/data/V300R004C68B009/xrocket/package/etcd.yaml",
		"/opt/data/V300R004C68B009/xrocket/package/solution.config",
		"/etc/hosts",
		"/etc/nats/simple.conf",
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("controlled product bundle=%v", actual)
	}
	copyActual := append([]string(nil), actual...)
	sort.Strings(copyActual)
	if len(copyActual) != 5 {
		t.Fatalf("unexpected controlled product bundle size=%d", len(copyActual))
	}
}

type unhealthyTextExecutor struct{ *productionFixtureExecutor }

func (fake *unhealthyTextExecutor) Execute(ctx context.Context, command executor.Command) (executor.Result, error) {
	if command.Name == "env" && len(command.Args) == 5 && command.Args[3] == "endpoint" && command.Args[4] == "health" {
		fake.commands = append(fake.commands, command)
		return executor.Result{Stdout: "endpoint is unhealthy\n"}, nil
	}
	return fake.productionFixtureExecutor.Execute(ctx, command)
}

func TestProductionRecoveryRejectsUnhealthySubstringWithoutCommandError(t *testing.T) {
	fs := newMappedRecoveryFilesystem(t)
	before := productionBeforeFixture()
	seedProductionFiles(t, fs, before)
	base := newProductionFixtureExecutor(fs)
	collector := &productionRecoveryCollector{executor: &unhealthyTextExecutor{productionFixtureExecutor: base}, fs: fs}
	request := productionCaptureRequest(t, 9, before)
	if _, err := collector.Capture(context.Background(), request); err == nil {
		t.Fatal("endpoint is unhealthy was accepted by recovery capture")
	}

	inspector := &productionReadOnlyInspector{executor: &unhealthyTextExecutor{productionFixtureExecutor: newProductionFixtureExecutor(fs)}, fs: fs}
	state := RollbackEtcdContract{
		ConfigPath: before.Etcd.ConfigPath, EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme,
		OldClient: before.Etcd.ClientAddress, ClientPort: before.Etcd.ClientPort,
		OldPeer: before.Etcd.PeerAddress, PeerPort: before.Etcd.PeerPort,
		MemberID: before.Etcd.MemberID, MemberCount: before.Etcd.MemberCount,
		ServiceName: before.Etcd.ServiceName, ControlAdapter: before.Etcd.ControlAdapter,
	}
	if _, err := inspector.EtcdRecoveryState(context.Background(), state); err == nil {
		t.Fatal("endpoint is unhealthy was accepted by read-only inspection")
	}
}
