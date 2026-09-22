//go:build linux

package xrocketreaddress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"setpoint/internal/executor"
)

const (
	recoveryFileArtifactSchema        = "xrocket.readdress.recovery-file.v1"
	maxRecoverySourceBytes            = int64(8 << 20)
	maxRecoveryBundleBytes            = int64(32 << 20)
	maxLogicalKVOutputBytes           = 1 << 20
	maxEtcdSnapshotBytes              = int64(512 << 20)
	maxEtcdSnapshotCommandOutputBytes = 64 << 10
)

type recoveryFilesystem interface {
	Lstat(string) (os.FileInfo, error)
	EvalSymlinks(string) (string, error)
	Open(string) (*os.File, error)
	OpenFile(string, int, os.FileMode) (*os.File, error)
	MkdirAll(string, os.FileMode) error
	Mkdir(string, os.FileMode) error
	Chmod(string, os.FileMode) error
	Rename(string, string) error
	Remove(string) error
	RemoveAll(string) error
}

type osRecoveryFilesystem struct{}

func (osRecoveryFilesystem) Lstat(name string) (os.FileInfo, error) { return os.Lstat(name) }
func (osRecoveryFilesystem) EvalSymlinks(name string) (string, error) {
	return filepath.EvalSymlinks(name)
}
func (osRecoveryFilesystem) Open(name string) (*os.File, error) { return os.Open(name) }
func (osRecoveryFilesystem) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(name, flag, perm)
}
func (osRecoveryFilesystem) MkdirAll(name string, perm os.FileMode) error {
	return os.MkdirAll(name, perm)
}
func (osRecoveryFilesystem) Mkdir(name string, perm os.FileMode) error { return os.Mkdir(name, perm) }
func (osRecoveryFilesystem) Chmod(name string, mode os.FileMode) error { return os.Chmod(name, mode) }
func (osRecoveryFilesystem) Rename(oldPath, newPath string) error      { return os.Rename(oldPath, newPath) }
func (osRecoveryFilesystem) Remove(name string) error                  { return os.Remove(name) }
func (osRecoveryFilesystem) RemoveAll(name string) error               { return os.RemoveAll(name) }

type recoveryFileEntry struct {
	SourcePath string `json:"source_path"`
	Mode       uint32 `json:"mode"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	LinkType   string `json:"link_type"`
	Data       []byte `json:"data"`
}

type recoveryFileArtifact struct {
	SchemaVersion string              `json:"schema_version"`
	Entries       []recoveryFileEntry `json:"entries"`
}

type productionRecoveryCollector struct {
	executor executor.CommandExecutor
	fs       recoveryFilesystem
}

type productionReadOnlyInspector struct {
	executor executor.CommandExecutor
	probe    discoveryProbe
	fs       recoveryFilesystem
	dial     func(context.Context, string, string) (net.Conn, error)
}

func newProductionRecoveryArtifactCollector(commandExecutor executor.CommandExecutor) (RecoveryArtifactCollector, error) {
	return &productionRecoveryCollector{executor: commandExecutor, fs: osRecoveryFilesystem{}}, nil
}

func newProductionReadOnlyInspector(commandExecutor executor.CommandExecutor) (ProductionReadOnlyInspector, error) {
	return &productionReadOnlyInspector{
		executor: commandExecutor,
		probe:    discoveryProbe{executor: commandExecutor},
		fs:       osRecoveryFilesystem{},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 2 * time.Second}
			return dialer.DialContext(ctx, network, address)
		},
	}, nil
}

func (collector *productionRecoveryCollector) Capture(ctx context.Context, request RecoveryArtifactCaptureRequest) (contract RecoveryArtifactContract, err error) {
	spec, err := validateProductionCaptureRequest(request)
	if err != nil {
		return RecoveryArtifactContract{}, err
	}
	contract = RecoveryArtifactContract{SchemaVersion: RecoveryArtifactContractSchema, Owner: request.Owner}
	if spec.Kind == stageKindSnapshot || spec.Kind == stageKindFinal {
		return contract, nil
	}
	if request.Before.Product.InstallProfile != installProfileRootNative {
		return RecoveryArtifactContract{}, errors.New("xRocket production recovery capture permits mutation support only for the root-native product envelope")
	}
	ownerRoot, err := collector.prepareOwnerRoot(request.Owner)
	if err != nil {
		return RecoveryArtifactContract{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = collector.fs.RemoveAll(ownerRoot)
		}
	}()

	captureFile := func(kind, source, name string) (RecoveryArtifactRef, error) {
		return collector.captureFileArtifact(request.Owner, ownerRoot, kind, source, name)
	}

	switch spec.Kind {
	case stageKindAlias:
		artifact, captureErr := captureFile(recoveryKindNetworkConfig, request.Before.Network.ConfigPath, "network-config.json")
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		contract.Artifacts = append(contract.Artifacts, artifact)
	case stageKindProduct:
		bundle, captureErr := collector.captureProductBundle(request.Owner, ownerRoot, request.Before)
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		keepalived, captureErr := captureFile(recoveryKindKeepalivedConfig, request.Before.HA.ConfigPath, "keepalived-config.json")
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		contract.Artifacts = append(contract.Artifacts, bundle, keepalived)
	case stageKindExternalDB:
		artifact, captureErr := captureFile(recoveryKindCommonYAML, request.Before.Product.CommonYAMLPath, "common-yaml.json")
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		contract.Artifacts = append(contract.Artifacts, artifact)
	case stageKindEtcd:
		config, captureErr := captureFile(recoveryKindEtcdConfig, request.Before.Etcd.ConfigPath, "etcd-config.json")
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		digestBefore, captureErr := collector.captureLogicalKVDigest(ctx, request.Before.Etcd)
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		snapshot, captureErr := collector.captureEtcdSnapshot(ctx, request.Owner, ownerRoot, request.Before.Etcd)
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		digestAfter, captureErr := collector.captureLogicalKVDigest(ctx, request.Before.Etcd)
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		if digestBefore != digestAfter {
			return RecoveryArtifactContract{}, errors.New("xRocket etcd logical KV state changed during snapshot capture")
		}
		contract.Artifacts = append(contract.Artifacts, config, snapshot)
		contract.LogicalKVDigest = digestBefore
	case stageKindConfd:
		destinations := append([]string(nil), request.Before.Rendering.ConfdDestinations...)
		sort.Strings(destinations)
		for index, destination := range destinations {
			artifact, captureErr := captureFile(recoveryKindConfdDestination, destination, fmt.Sprintf("confd-%03d.json", index))
			if captureErr != nil {
				return RecoveryArtifactContract{}, captureErr
			}
			contract.Artifacts = append(contract.Artifacts, artifact)
		}
	case stageKindOS:
		artifact, captureErr := captureFile(recoveryKindNetworkConfig, request.Before.Network.ConfigPath, "network-config.json")
		if captureErr != nil {
			return RecoveryArtifactContract{}, captureErr
		}
		contract.Artifacts = append(contract.Artifacts, artifact)
	default:
		return RecoveryArtifactContract{}, fmt.Errorf("unsupported xRocket production recovery stage %q", spec.Kind)
	}

	sort.Slice(contract.Artifacts, func(left, right int) bool {
		if contract.Artifacts[left].Kind != contract.Artifacts[right].Kind {
			return contract.Artifacts[left].Kind < contract.Artifacts[right].Kind
		}
		return contract.Artifacts[left].SourcePath < contract.Artifacts[right].SourcePath
	})
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
		Recovery:           &contract,
	}
	if err := validateRecoveryContract(manifest); err != nil {
		return RecoveryArtifactContract{}, err
	}
	complete = true
	return contract, nil
}

func validateProductionCaptureRequest(request RecoveryArtifactCaptureRequest) (canonicalStage, error) {
	owner := request.Owner
	if strings.TrimSpace(owner.RunID) == "" || strings.TrimSpace(owner.NodeID) == "" || strings.TrimSpace(owner.StageID) == "" || !validSHA256Digest(owner.OwnerID) {
		return canonicalStage{}, errors.New("xRocket production recovery owner identity is incomplete")
	}
	if owner.StageIndex < 0 || owner.StageIndex >= len(canonicalStages) {
		return canonicalStage{}, errors.New("xRocket production recovery stage index is invalid")
	}
	if owner.Role != "master" && owner.Role != "slave" {
		return canonicalStage{}, errors.New("xRocket production recovery role is invalid")
	}
	participants := append([]string(nil), owner.ParticipantNodeIDs...)
	if len(participants) != 2 || participants[0] == participants[1] || owner.NodeID != participants[0] && owner.NodeID != participants[1] {
		return canonicalStage{}, errors.New("xRocket production recovery participants are invalid")
	}
	sortedParticipants := append([]string(nil), participants...)
	sort.Strings(sortedParticipants)
	if !reflect.DeepEqual(participants, sortedParticipants) {
		return canonicalStage{}, errors.New("xRocket production recovery participants are not canonicalized")
	}
	spec := canonicalStages[owner.StageIndex]
	if spec.ID != owner.StageID || spec.Role != owner.Role || string(spec.Kind) != request.StageKind {
		return canonicalStage{}, errors.New("xRocket production recovery owner/stage correlation is invalid")
	}
	manifest := restorePointManifest{
		RunID:              owner.RunID,
		StageID:            owner.StageID,
		StageIndex:         owner.StageIndex,
		NodeID:             owner.NodeID,
		ParticipantNodeIDs: participants,
		Role:               owner.Role,
	}
	if recoveryOwnerID(manifest) != owner.OwnerID {
		return canonicalStage{}, errors.New("xRocket production recovery owner digest does not match run/stage identity")
	}
	if err := validateRestoreBeforeState(request.Before); err != nil {
		return canonicalStage{}, err
	}
	return spec, nil
}

func (collector *productionRecoveryCollector) prepareOwnerRoot(owner RecoveryArtifactOwner) (string, error) {
	if err := collector.fs.MkdirAll(recoveryArtifactRoot, 0o700); err != nil {
		return "", fmt.Errorf("prepare xRocket recovery root: %w", err)
	}
	if err := requireExactDirectory(collector.fs, recoveryArtifactRoot, 0o700); err != nil {
		return "", err
	}
	ownerRoot := recoveryBackupRoot(owner.OwnerID)
	if path.Dir(ownerRoot) != recoveryArtifactRoot || !strings.HasPrefix(ownerRoot, recoveryArtifactRoot+"/") {
		return "", errors.New("xRocket recovery owner root escaped the frozen recovery root")
	}
	if err := collector.fs.Mkdir(ownerRoot, 0o700); err != nil {
		return "", fmt.Errorf("create immutable xRocket recovery owner root: %w", err)
	}
	if err := requireExactDirectory(collector.fs, ownerRoot, 0o700); err != nil {
		_ = collector.fs.RemoveAll(ownerRoot)
		return "", err
	}
	return ownerRoot, nil
}

func requireExactDirectory(fs recoveryFilesystem, name string, mode os.FileMode) error {
	info, err := fs.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect xRocket recovery directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("xRocket recovery directory is not an exact local directory")
	}
	resolved, err := fs.EvalSymlinks(name)
	if err != nil || resolved != name {
		return errors.New("xRocket recovery directory contains path/symlink ambiguity")
	}
	if info.Mode().Perm() != mode.Perm() {
		if err := fs.Chmod(name, mode); err != nil {
			return fmt.Errorf("restrict xRocket recovery directory permissions: %w", err)
		}
	}
	return nil
}

func (collector *productionRecoveryCollector) captureFileArtifact(owner RecoveryArtifactOwner, ownerRoot, kind, source, name string) (RecoveryArtifactRef, error) {
	entry, err := readExactRecoverySource(collector.fs, source)
	if err != nil {
		return RecoveryArtifactRef{}, err
	}
	artifact := recoveryFileArtifact{SchemaVersion: recoveryFileArtifactSchema, Entries: []recoveryFileEntry{entry}}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return RecoveryArtifactRef{}, err
	}
	backupRef, digest, err := collector.writeAtomic(ownerRoot, name, payload)
	if err != nil {
		return RecoveryArtifactRef{}, err
	}
	return makeRecoveryArtifactRef(owner, kind, source, backupRef, digest), nil
}

func (collector *productionRecoveryCollector) captureProductBundle(owner RecoveryArtifactOwner, ownerRoot string, before restoreBeforeState) (RecoveryArtifactRef, error) {
	if before.Product.InstallProfile != installProfileRootNative || before.Product.ProductPrefix != "" {
		return RecoveryArtifactRef{}, errors.New("xRocket production product recovery bundle is root-native only")
	}
	if err := requireExactSourceDirectory(collector.fs, before.Product.VersionEvidence); err != nil {
		return RecoveryArtifactRef{}, err
	}
	paths := controlledProductRecoveryPaths(before)
	entries := make([]recoveryFileEntry, 0, len(paths))
	var total int64
	for _, source := range paths {
		entry, err := readExactRecoverySource(collector.fs, source)
		if err != nil {
			return RecoveryArtifactRef{}, err
		}
		total += int64(len(entry.Data))
		if total > maxRecoveryBundleBytes {
			return RecoveryArtifactRef{}, errors.New("xRocket product recovery bundle exceeds the bounded capture limit")
		}
		entries = append(entries, entry)
	}
	artifact := recoveryFileArtifact{SchemaVersion: recoveryFileArtifactSchema, Entries: entries}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return RecoveryArtifactRef{}, err
	}
	backupRef, digest, err := collector.writeAtomic(ownerRoot, "product-config-bundle.json", payload)
	if err != nil {
		return RecoveryArtifactRef{}, err
	}
	return makeRecoveryArtifactRef(owner, recoveryKindProductBundle, before.Product.VersionEvidence, backupRef, digest), nil
}

func controlledProductRecoveryPaths(before restoreBeforeState) []string {
	return []string{
		path.Join(before.Product.VersionEvidence, "package/nodes.yaml"),
		path.Join(before.Product.VersionEvidence, "package/etcd.yaml"),
		path.Join(before.Product.VersionEvidence, "package/solution.config"),
		prefixed(before.Product.ProductPrefix, "/etc/hosts"),
		prefixed(before.Product.ProductPrefix, "/etc/nats/simple.conf"),
	}
}

func makeRecoveryArtifactRef(owner RecoveryArtifactOwner, kind, source, backupRef, digest string) RecoveryArtifactRef {
	value := RecoveryArtifactRef{OwnerID: owner.OwnerID, Kind: kind, SourcePath: source, BackupRef: backupRef, SHA256: digest}
	value.ID = recoveryArtifactID(value)
	return value
}

func requireExactSourceDirectory(fs recoveryFilesystem, source string) error {
	if !boundedAbsolutePath(source) || strings.HasPrefix(source, recoveryArtifactRoot+"/") || source == recoveryArtifactRoot {
		return errors.New("xRocket recovery source directory is outside the frozen source boundary")
	}
	info, err := fs.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect xRocket recovery source directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("xRocket recovery source directory is not an exact directory")
	}
	resolved, err := fs.EvalSymlinks(source)
	if err != nil || resolved != source {
		return errors.New("xRocket recovery source directory contains symlink/path ambiguity")
	}
	return nil
}

func readExactRecoverySource(fs recoveryFilesystem, source string) (recoveryFileEntry, error) {
	if !boundedAbsolutePath(source) || strings.HasPrefix(source, recoveryArtifactRoot+"/") || source == recoveryArtifactRoot {
		return recoveryFileEntry{}, errors.New("xRocket recovery source path is outside the frozen source boundary")
	}
	info, err := fs.Lstat(source)
	if err != nil {
		return recoveryFileEntry{}, fmt.Errorf("inspect xRocket recovery source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return recoveryFileEntry{}, errors.New("xRocket recovery source must be one exact regular file")
	}
	resolved, err := fs.EvalSymlinks(source)
	if err != nil || resolved != source {
		return recoveryFileEntry{}, errors.New("xRocket recovery source contains symlink/path ambiguity")
	}
	if info.Size() < 0 || info.Size() > maxRecoverySourceBytes {
		return recoveryFileEntry{}, errors.New("xRocket recovery source exceeds the bounded capture limit")
	}
	file, err := fs.Open(source)
	if err != nil {
		return recoveryFileEntry{}, fmt.Errorf("open xRocket recovery source: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecoverySourceBytes+1))
	if err != nil {
		return recoveryFileEntry{}, fmt.Errorf("read xRocket recovery source: %w", err)
	}
	if int64(len(data)) > maxRecoverySourceBytes || int64(len(data)) != info.Size() {
		return recoveryFileEntry{}, errors.New("xRocket recovery source changed or exceeded the bounded capture limit")
	}
	uid, gid, err := fileOwnership(info)
	if err != nil {
		return recoveryFileEntry{}, err
	}
	return recoveryFileEntry{SourcePath: source, Mode: uint32(info.Mode().Perm()), UID: uid, GID: gid, LinkType: "regular", Data: data}, nil
}

func fileOwnership(info os.FileInfo) (uint32, uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, 0, errors.New("xRocket recovery source ownership is unavailable")
	}
	return stat.Uid, stat.Gid, nil
}

func (collector *productionRecoveryCollector) writeAtomic(ownerRoot, name string, payload []byte) (string, string, error) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return "", "", errors.New("xRocket recovery artifact name is invalid")
	}
	finalPath := path.Join(ownerRoot, name)
	if path.Dir(finalPath) != ownerRoot {
		return "", "", errors.New("xRocket recovery artifact escaped its owner root")
	}
	tempPath := finalPath + ".tmp"
	file, err := collector.fs.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("create xRocket recovery artifact temp file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = collector.fs.Remove(tempPath)
		}
	}()
	if _, err := file.Write(payload); err != nil {
		return "", "", fmt.Errorf("write xRocket recovery artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", "", fmt.Errorf("sync xRocket recovery artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", "", fmt.Errorf("close xRocket recovery artifact: %w", err)
	}
	if err := collector.fs.Chmod(tempPath, 0o600); err != nil {
		return "", "", fmt.Errorf("restrict xRocket recovery artifact permissions: %w", err)
	}
	if err := collector.fs.Rename(tempPath, finalPath); err != nil {
		return "", "", fmt.Errorf("commit xRocket recovery artifact atomically: %w", err)
	}
	cleanup = false
	if err := syncDirectory(collector.fs, ownerRoot); err != nil {
		return "", "", err
	}
	info, err := collector.fs.Lstat(finalPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", "", errors.New("xRocket recovery artifact permissions/type are invalid after commit")
	}
	digest, err := hashFile(collector.fs, finalPath)
	if err != nil {
		return "", "", err
	}
	return finalPath, digest, nil
}

func syncDirectory(fs recoveryFilesystem, directory string) error {
	file, err := fs.Open(directory)
	if err != nil {
		return fmt.Errorf("open xRocket recovery directory for sync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync xRocket recovery directory: %w", err)
	}
	return nil
}

func hashFile(fs recoveryFilesystem, name string) (string, error) {
	file, err := fs.Open(name)
	if err != nil {
		return "", fmt.Errorf("open xRocket recovery artifact for hash: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash xRocket recovery artifact: %w", err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (collector *productionRecoveryCollector) captureLogicalKVDigest(ctx context.Context, state restoreEtcdState) (string, error) {
	if err := collector.validateEtcdIdentity(ctx, state); err != nil {
		return "", err
	}
	endpoint := etcdEndpoint(state.Scheme, state.ClientAddress, state.ClientPort)
	result, err := collector.executor.Execute(ctx, executor.Command{
		Name:        "env",
		Args:        []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "get", "", "--prefix", "-w", "json"},
		OutputLimit: maxLogicalKVOutputBytes,
	})
	if err != nil {
		return "", errors.New("xRocket logical KV digest capture failed")
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", errors.New("xRocket logical KV digest output exceeded the bounded capture limit")
	}
	return deterministicKVDigest([]byte(result.Stdout))
}

func (collector *productionRecoveryCollector) validateEtcdIdentity(ctx context.Context, state restoreEtcdState) error {
	entry, err := readExactRecoverySource(collector.fs, state.ConfigPath)
	if err != nil {
		return err
	}
	parsed, err := parseEtcdConfig(string(entry.Data), state.ClientAddress)
	if err != nil || parsed.Scheme != state.Scheme || parsed.ClientPort != state.ClientPort || parsed.PeerPort != state.PeerPort {
		return errors.New("xRocket etcd config identity differs from the frozen RestorePoint")
	}
	endpoint := etcdEndpoint(state.Scheme, state.ClientAddress, state.ClientPort)
	health, err := collector.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "endpoint", "health"}})
	if err != nil || health.StdoutTruncated || health.StderrTruncated || !strictEtcdHealthOutput(health.Stdout) {
		return errors.New("xRocket etcd health does not match the frozen recovery precondition")
	}
	members, err := collector.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "member", "list", "-w", "json"}, OutputLimit: maxLogicalKVOutputBytes})
	if err != nil || members.StdoutTruncated || members.StderrTruncated {
		return errors.New("xRocket etcd member identity capture failed")
	}
	memberID, memberCount, err := parseEtcdMemberList(members.Stdout, state.PeerAddress, state.PeerPort)
	if err != nil || memberID != state.MemberID || memberCount != state.MemberCount || state.MemberCount != 1 {
		return errors.New("xRocket etcd member identity/count differs from the frozen RestorePoint")
	}
	return nil
}

func (collector *productionRecoveryCollector) captureEtcdSnapshot(ctx context.Context, owner RecoveryArtifactOwner, ownerRoot string, state restoreEtcdState) (RecoveryArtifactRef, error) {
	if err := collector.validateEtcdIdentity(ctx, state); err != nil {
		return RecoveryArtifactRef{}, err
	}
	tempPath := path.Join(ownerRoot, "etcd-snapshot.db.tmp")
	finalPath := path.Join(ownerRoot, "etcd-snapshot.db")
	endpoint := etcdEndpoint(state.Scheme, state.ClientAddress, state.ClientPort)
	result, err := collector.executor.Execute(ctx, executor.Command{
		Name:        "env",
		Args:        []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "snapshot", "save", tempPath},
		OutputLimit: maxEtcdSnapshotCommandOutputBytes,
	})
	if err != nil {
		return RecoveryArtifactRef{}, errors.New("xRocket etcd snapshot capture failed")
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return RecoveryArtifactRef{}, errors.New("xRocket etcd snapshot command output exceeded the bounded capture limit")
	}
	info, err := collector.fs.Lstat(tempPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return RecoveryArtifactRef{}, errors.New("xRocket etcd snapshot output is missing or not a regular file")
	}
	if info.Size() > maxEtcdSnapshotBytes {
		return RecoveryArtifactRef{}, errors.New("xRocket etcd snapshot exceeds the bounded artifact size limit")
	}
	resolved, err := collector.fs.EvalSymlinks(tempPath)
	if err != nil || resolved != tempPath {
		return RecoveryArtifactRef{}, errors.New("xRocket etcd snapshot output contains path/symlink ambiguity")
	}
	if err := collector.fs.Chmod(tempPath, 0o600); err != nil {
		return RecoveryArtifactRef{}, fmt.Errorf("restrict xRocket etcd snapshot permissions: %w", err)
	}
	file, err := collector.fs.Open(tempPath)
	if err != nil {
		return RecoveryArtifactRef{}, fmt.Errorf("open xRocket etcd snapshot for durable sync: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return RecoveryArtifactRef{}, fmt.Errorf("sync xRocket etcd snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return RecoveryArtifactRef{}, fmt.Errorf("close xRocket etcd snapshot: %w", err)
	}
	if err := collector.fs.Rename(tempPath, finalPath); err != nil {
		return RecoveryArtifactRef{}, fmt.Errorf("commit xRocket etcd snapshot atomically: %w", err)
	}
	if err := syncDirectory(collector.fs, ownerRoot); err != nil {
		return RecoveryArtifactRef{}, err
	}
	committedInfo, err := collector.fs.Lstat(finalPath)
	if err != nil || !committedInfo.Mode().IsRegular() || committedInfo.Size() <= 0 || committedInfo.Size() > maxEtcdSnapshotBytes {
		return RecoveryArtifactRef{}, errors.New("xRocket committed etcd snapshot violates the bounded artifact size limit")
	}
	digest, err := hashFile(collector.fs, finalPath)
	if err != nil {
		return RecoveryArtifactRef{}, err
	}
	return makeRecoveryArtifactRef(owner, recoveryKindEtcdSnapshot, state.EtcdctlPath, finalPath, digest), nil
}

func deterministicKVDigest(payload []byte) (string, error) {
	var response struct {
		KVs []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"kvs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&response); err != nil {
		return "", errors.New("xRocket logical KV response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("xRocket logical KV response contains trailing data")
	}
	type kv struct{ key, value []byte }
	values := make([]kv, 0, len(response.KVs))
	seen := map[string]struct{}{}
	for _, item := range response.KVs {
		key, err := base64.StdEncoding.DecodeString(item.Key)
		if err != nil {
			return "", errors.New("xRocket logical KV key encoding is invalid")
		}
		value, err := base64.StdEncoding.DecodeString(item.Value)
		if err != nil {
			return "", errors.New("xRocket logical KV value encoding is invalid")
		}
		identity := string(key)
		if _, duplicate := seen[identity]; duplicate {
			return "", errors.New("xRocket logical KV response contains duplicate keys")
		}
		seen[identity] = struct{}{}
		values = append(values, kv{key: key, value: value})
	}
	sort.Slice(values, func(left, right int) bool { return bytes.Compare(values[left].key, values[right].key) < 0 })
	hash := sha256.New()
	var length [8]byte
	for _, item := range values {
		binary.BigEndian.PutUint64(length[:], uint64(len(item.key)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(item.key)
		binary.BigEndian.PutUint64(length[:], uint64(len(item.value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(item.value)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func etcdEndpoint(scheme, address string, portValue int) string {
	return fmt.Sprintf("%s://%s:%d", scheme, address, portValue)
}

func strictEtcdHealthOutput(stdout string) bool {
	value := strings.ToLower(strings.TrimSpace(stdout))
	if value == "" || strings.Contains(value, "unhealthy") || strings.Contains(value, "error") || strings.Contains(value, "failed") {
		return false
	}
	return regexp.MustCompile(`(?m)(^|[[:space:]])is[[:space:]]+healthy([[:space:]]|:|$)`).MatchString(value)
}

func (inspector *productionReadOnlyInspector) AliasSatisfied(ctx context.Context, contract AliasStageContract) (bool, error) {
	observation, err := inspector.probe.observeNetwork(ctx)
	if err != nil {
		return false, err
	}
	if !networkHasAddress(observation, contract.Interface, contract.OldAddress, contract.PrefixLength) || !networkHasAddress(observation, contract.Interface, contract.NewAddress, contract.PrefixLength) {
		return false, nil
	}
	return networkHasDefaultRoute(observation, contract.Interface, contract.Gateway), nil
}

func (inspector *productionReadOnlyInspector) ProductSatisfied(_ context.Context, contract ProductStageContract) (bool, error) {
	if contract.InstallProfile != installProfileRootNative && contract.InstallProfile != installProfileHomePrefix {
		return false, errors.New("xRocket product inspection profile is unsupported")
	}
	if !hasRecoveryArtifactRef(contract.ProductBundle) || !hasRecoveryArtifactRef(contract.KeepalivedConfig) {
		return false, errors.New("xRocket product inspection requires run-owned v2 recovery baselines")
	}
	bundle, err := inspector.readVerifiedRecoveryFileArtifact(contract.ProductBundle, recoveryKindProductBundle)
	if err != nil {
		return false, err
	}
	expectedSources := []string{
		path.Join(contract.ProductBundle.SourcePath, "package/nodes.yaml"),
		path.Join(contract.ProductBundle.SourcePath, "package/etcd.yaml"),
		path.Join(contract.ProductBundle.SourcePath, "package/solution.config"),
		prefixed(contract.ProductPrefix, "/etc/hosts"),
		prefixed(contract.ProductPrefix, "/etc/nats/simple.conf"),
	}
	substitutions := []addressSubstitution{
		{Old: contract.OldMaster, New: contract.NewMaster},
		{Old: contract.OldSlave, New: contract.NewSlave},
		{Old: contract.OldVIP, New: contract.NewVIP},
	}
	matched, err := inspector.verifyRecoveryEntriesTransition(bundle.Entries, expectedSources, substitutions, true)
	if err != nil || !matched {
		return matched, err
	}
	keepalived, err := inspector.readVerifiedRecoveryFileArtifact(contract.KeepalivedConfig, recoveryKindKeepalivedConfig)
	if err != nil {
		return false, err
	}
	matched, err = inspector.verifyRecoveryEntriesTransition(keepalived.Entries, []string{contract.KeepalivedConfig.SourcePath}, substitutions, true)
	if err != nil || !matched {
		return matched, err
	}
	current, err := readObservationBytes(inspector.fs, contract.KeepalivedConfig.SourcePath)
	if err != nil {
		return false, err
	}
	ha, err := parseKeepalivedConfig(string(current))
	if err != nil {
		return false, err
	}
	expectedSource, expectedPeer := contract.NewMaster, contract.NewSlave
	if contract.Role == "slave" {
		expectedSource, expectedPeer = contract.NewSlave, contract.NewMaster
	}
	return ha.SourceAddress == expectedSource && ha.PeerAddress == expectedPeer && ha.VIPAddress == contract.NewVIP && ha.Nopreempt, nil
}

func (inspector *productionReadOnlyInspector) ExternalDBSatisfied(_ context.Context, contract ExternalDBStageContract) (bool, error) {
	if !contract.PreserveNonAddressFields {
		return false, errors.New("xRocket external DB inspection requires non-address preservation contract")
	}
	if !hasRecoveryArtifactRef(contract.BaselineConfig) {
		return false, errors.New("xRocket external DB inspection requires a run-owned v2 common.yaml baseline")
	}
	baseline, err := inspector.readVerifiedRecoveryFileArtifact(contract.BaselineConfig, recoveryKindCommonYAML)
	if err != nil {
		return false, err
	}
	if len(baseline.Entries) != 1 || baseline.Entries[0].SourcePath != contract.CommonYAMLPath {
		return false, errors.New("xRocket external DB recovery baseline does not match the frozen common.yaml path")
	}
	current, err := readObservationBytes(inspector.fs, contract.CommonYAMLPath)
	if err != nil {
		return false, err
	}
	return verifyExternalDBAddressOnlyTransition(baseline.Entries[0].Data, current, contract.OldAddress, contract.NewAddress, contract.Port)
}

func (inspector *productionReadOnlyInspector) EtcdSatisfied(ctx context.Context, contract EtcdStageContract) (bool, error) {
	state := restoreEtcdState{
		ConfigPath: contract.ConfigPath, EtcdctlPath: contract.EtcdctlPath, Scheme: contract.Scheme,
		ClientAddress: contract.NewClient, ClientPort: contract.ClientPort,
		PeerAddress: contract.NewPeer, PeerPort: contract.PeerPort,
		MemberID: contract.MemberID, MemberCount: contract.MemberCount,
		ServiceName: contract.ServiceName, ControlAdapter: contract.ControlAdapter,
	}
	observation, err := inspector.observeEtcd(ctx, state)
	if err != nil {
		return false, err
	}
	return observation.Healthy && observation.MemberID == contract.MemberID && observation.MemberCount == 1 && observation.ClientAddress == contract.NewClient && observation.PeerAddress == contract.NewPeer && validSHA256Digest(observation.LogicalKVDigest), nil
}

func (inspector *productionReadOnlyInspector) ConfdSatisfied(_ context.Context, contract ConfdStageContract) (bool, error) {
	if len(contract.DestinationFiles) == 0 || len(contract.DestinationFiles) != len(contract.Destinations) {
		return false, errors.New("xRocket confd inspection requires one run-owned v2 baseline per frozen destination")
	}
	substitutions := []addressSubstitution{
		{Old: contract.OldMaster, New: contract.ExpectedMaster},
		{Old: contract.OldSlave, New: contract.ExpectedSlave},
		{Old: contract.OldVIP, New: contract.ExpectedVIP},
		{Old: contract.OldExternalDB, New: contract.ExpectedExternalDB},
	}
	total := make([]int, len(substitutions))
	for index, destination := range contract.Destinations {
		ref := contract.DestinationFiles[index]
		if ref.SourcePath != destination {
			return false, errors.New("xRocket confd recovery baseline destination correlation is invalid")
		}
		baseline, err := inspector.readVerifiedRecoveryFileArtifact(ref, recoveryKindConfdDestination)
		if err != nil {
			return false, err
		}
		matched, counts, err := inspector.verifyRecoveryEntriesTransitionWithCounts(baseline.Entries, []string{destination}, substitutions)
		if err != nil || !matched {
			return matched, err
		}
		for subIndex, count := range counts {
			total[subIndex] += count
		}
	}
	for _, count := range total {
		if count == 0 {
			return false, nil
		}
	}
	return true, nil
}

func (inspector *productionReadOnlyInspector) OSSatisfied(ctx context.Context, contract OSStageContract) (bool, error) {
	data, err := readObservationBytes(inspector.fs, contract.ConfigPath)
	if err != nil {
		return false, err
	}
	address, prefix, gateway, err := parseIfcfg(string(data))
	if err != nil {
		return false, err
	}
	if address != contract.NewAddress || prefix != contract.PrefixLength || gateway != contract.Gateway {
		return false, nil
	}
	observation, err := inspector.probe.observeNetwork(ctx)
	if err != nil {
		return false, err
	}
	if !networkHasAddress(observation, contract.Interface, contract.NewAddress, contract.PrefixLength) || networkContainsAddress(observation, contract.OldAddress) {
		return false, nil
	}
	return networkHasDefaultRoute(observation, contract.Interface, contract.Gateway), nil
}

func (inspector *productionReadOnlyInspector) FinalSiteSatisfied(ctx context.Context, contract FinalSiteStageContract) (bool, error) {
	_, content, found, err := inspector.probe.readKeepalivedConfig(ctx)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	ha, err := parseKeepalivedConfig(content)
	if err != nil {
		return false, err
	}
	expectedSource, expectedPeer := contract.MasterAddress, contract.SlaveAddress
	if contract.Role == "slave" {
		expectedSource, expectedPeer = contract.SlaveAddress, contract.MasterAddress
	}
	ports := append([]int(nil), contract.BusinessPorts...)
	observedPorts := append([]int(nil), ha.BusinessPorts...)
	sort.Ints(ports)
	sort.Ints(observedPorts)
	if ha.SourceAddress != expectedSource || ha.PeerAddress != expectedPeer || ha.VIPAddress != contract.VIPAddress || ha.Nopreempt != contract.Nopreempt || !reflect.DeepEqual(ports, observedPorts) {
		return false, nil
	}
	networkState, err := inspector.probe.observeNetwork(ctx)
	if err != nil {
		return false, err
	}
	if !networkContainsAddress(networkState, expectedSource) || !networkContainsAddress(networkState, contract.VIPAddress) {
		return false, nil
	}
	for _, portValue := range ports {
		conn, dialErr := inspector.dial(ctx, "tcp", net.JoinHostPort(contract.VIPAddress, fmt.Sprintf("%d", portValue)))
		if dialErr != nil {
			return false, nil
		}
		_ = conn.Close()
	}
	return true, nil
}

type addressSubstitution struct {
	Old string
	New string
}

func (inspector *productionReadOnlyInspector) readVerifiedRecoveryFileArtifact(ref RecoveryArtifactRef, expectedKind string) (recoveryFileArtifact, error) {
	if ref.Kind != expectedKind {
		return recoveryFileArtifact{}, errors.New("xRocket recovery baseline kind does not match the production inspection contract")
	}
	if err := validateRecoveryRefPath(ref); err != nil {
		return recoveryFileArtifact{}, err
	}
	digest, err := hashFile(inspector.fs, ref.BackupRef)
	if err != nil {
		return recoveryFileArtifact{}, err
	}
	if digest != ref.SHA256 {
		return recoveryFileArtifact{}, errors.New("xRocket recovery baseline bytes do not match the frozen SHA")
	}
	return readRecoveryFileArtifact(inspector.fs, ref.BackupRef)
}

func (inspector *productionReadOnlyInspector) verifyRecoveryEntriesTransition(entries []recoveryFileEntry, expectedSources []string, substitutions []addressSubstitution, requireEverySubstitution bool) (bool, error) {
	matched, counts, err := inspector.verifyRecoveryEntriesTransitionWithCounts(entries, expectedSources, substitutions)
	if err != nil || !matched || !requireEverySubstitution {
		return matched, err
	}
	for _, count := range counts {
		if count == 0 {
			return false, nil
		}
	}
	return true, nil
}

func (inspector *productionReadOnlyInspector) verifyRecoveryEntriesTransitionWithCounts(entries []recoveryFileEntry, expectedSources []string, substitutions []addressSubstitution) (bool, []int, error) {
	if len(entries) != len(expectedSources) {
		return false, nil, errors.New("xRocket recovery baseline file cardinality differs from the frozen controlled set")
	}
	counts := make([]int, len(substitutions))
	for index, baseline := range entries {
		if baseline.SourcePath != expectedSources[index] {
			return false, nil, errors.New("xRocket recovery baseline source path differs from the frozen controlled set")
		}
		current, err := readExactRecoverySource(inspector.fs, baseline.SourcePath)
		if err != nil {
			return false, nil, err
		}
		expectedBytes, entryCounts, err := applyBoundedAddressSubstitutions(baseline.Data, substitutions)
		if err != nil {
			return false, nil, err
		}
		for subIndex, count := range entryCounts {
			counts[subIndex] += count
		}
		expected := baseline
		expected.Data = expectedBytes
		if !reflect.DeepEqual(current, expected) {
			return false, counts, nil
		}
	}
	return true, counts, nil
}

func applyBoundedAddressSubstitutions(baseline []byte, substitutions []addressSubstitution) ([]byte, []int, error) {
	current := string(baseline)
	counts := make([]int, len(substitutions))
	for index, substitution := range substitutions {
		oldAddress := canonicalIPv4(substitution.Old)
		newAddress := canonicalIPv4(substitution.New)
		if oldAddress == "" || newAddress == "" || oldAddress == newAddress {
			return nil, nil, errors.New("xRocket production inspection address substitution is invalid")
		}
		if countIPv4Token(current, newAddress) > 0 {
			return nil, nil, errors.New("xRocket recovery baseline already contains a target address and is ambiguous")
		}
		var count int
		current, count = replaceIPv4Token(current, oldAddress, newAddress)
		counts[index] = count
	}
	return []byte(current), counts, nil
}

func replaceIPv4Token(input, oldAddress, newAddress string) (string, int) {
	var output strings.Builder
	start := 0
	count := 0
	for {
		relative := strings.Index(input[start:], oldAddress)
		if relative < 0 {
			output.WriteString(input[start:])
			break
		}
		index := start + relative
		end := index + len(oldAddress)
		if (index == 0 || !ipv4TokenByte(input[index-1])) && (end == len(input) || !ipv4TokenByte(input[end])) {
			output.WriteString(input[start:index])
			output.WriteString(newAddress)
			start = end
			count++
			continue
		}
		output.WriteString(input[start:end])
		start = end
	}
	return output.String(), count
}

func countIPv4Token(input, address string) int {
	_, count := replaceIPv4Token(input, address, address)
	return count
}

func ipv4TokenByte(value byte) bool {
	return value == '.' || value >= '0' && value <= '9'
}

type goldendbAddressObservation struct {
	Address string
	Port    int
	Start   int
	End     int
}

func goldendbAddressOnlyObservation(content string) (goldendbAddressObservation, error) {
	block, err := topLevelYAMLBlock(content, "goldendb")
	if err != nil {
		return goldendbAddressObservation{}, err
	}
	blockStart := strings.Index(content, block)
	if blockStart < 0 {
		return goldendbAddressObservation{}, errors.New("xRocket goldendb block cannot be correlated to the source bytes")
	}
	fieldPattern := regexp.MustCompile(`(?im)^[ \t]*(host|hostname|address|ip|endpoint)[ \t]*:[ \t]*(.*?)[ \t]*(?:#[^\r\n]*)?$`)
	ipPattern := regexp.MustCompile(`(?:[0-9]{1,3}\.){3}[0-9]{1,3}`)
	var address string
	start, end := -1, -1
	occurrences := 0
	for _, match := range fieldPattern.FindAllStringSubmatchIndex(block, -1) {
		if len(match) < 6 || match[4] < 0 {
			continue
		}
		value := block[match[4]:match[5]]
		for _, ipIndex := range ipPattern.FindAllStringIndex(value, -1) {
			candidate := canonicalIPv4(value[ipIndex[0]:ipIndex[1]])
			if candidate == "" {
				continue
			}
			occurrences++
			address = candidate
			start = blockStart + match[4] + ipIndex[0]
			end = blockStart + match[4] + ipIndex[1]
		}
	}
	portPattern := regexp.MustCompile(`(?im)^[ \t]*port[ \t]*:[ \t]*["']?([0-9]+)["']?[ \t]*(?:#[^\r\n]*)?$`)
	ports := portPattern.FindAllStringSubmatch(block, -1)
	if occurrences != 1 || len(ports) != 1 {
		return goldendbAddressObservation{}, fmt.Errorf("xRocket goldendb requires exactly one authoritative address and port, found %d/%d", occurrences, len(ports))
	}
	portValue, err := strconv.Atoi(ports[0][1])
	if err != nil || portValue < 1 || portValue > 65535 {
		return goldendbAddressObservation{}, errors.New("xRocket goldendb authoritative port is invalid")
	}
	return goldendbAddressObservation{Address: address, Port: portValue, Start: start, End: end}, nil
}

func verifyExternalDBAddressOnlyTransition(baseline, current []byte, oldAddress, newAddress string, portValue int) (bool, error) {
	before, err := goldendbAddressOnlyObservation(string(baseline))
	if err != nil {
		return false, err
	}
	after, err := goldendbAddressOnlyObservation(string(current))
	if err != nil {
		return false, err
	}
	if before.Address != canonicalIPv4(oldAddress) || after.Address != canonicalIPv4(newAddress) || before.Port != portValue || after.Port != portValue {
		return false, nil
	}
	expected := string(baseline[:before.Start]) + canonicalIPv4(newAddress) + string(baseline[before.End:])
	return expected == string(current), nil
}

func (inspector *productionReadOnlyInspector) RecoveryArtifactMatches(_ context.Context, artifact RecoveryArtifactRef) (bool, error) {
	if err := validateRecoveryRefPath(artifact); err != nil {
		return false, err
	}
	digest, err := hashFile(inspector.fs, artifact.BackupRef)
	if err != nil {
		return false, err
	}
	if digest != artifact.SHA256 {
		return false, nil
	}
	if artifact.Kind == recoveryKindEtcdSnapshot {
		return true, nil
	}
	_, err = readRecoveryFileArtifact(inspector.fs, artifact.BackupRef)
	return err == nil, err
}

func validateRecoveryRefPath(artifact RecoveryArtifactRef) error {
	if !validSHA256Digest(artifact.OwnerID) || !validSHA256Digest(artifact.SHA256) || !validSHA256Digest(artifact.ID) || artifact.ID != recoveryArtifactID(artifact) {
		return errors.New("xRocket recovery artifact identity is invalid")
	}
	root := recoveryBackupRoot(artifact.OwnerID)
	if !boundedAbsolutePath(artifact.BackupRef) || path.Dir(artifact.BackupRef) != root || !strings.HasPrefix(artifact.BackupRef, root+"/") {
		return errors.New("xRocket recovery artifact is outside its run-owned root")
	}
	return nil
}

func (inspector *productionReadOnlyInspector) EtcdRecoveryState(ctx context.Context, contract RollbackEtcdContract) (EtcdRecoveryObservation, error) {
	data, err := readObservationBytes(inspector.fs, contract.ConfigPath)
	if err != nil {
		return EtcdRecoveryObservation{}, err
	}
	current, err := parseMutationEtcdConfig(data)
	if err != nil {
		return EtcdRecoveryObservation{}, err
	}
	if current.scheme != contract.Scheme || current.clientPort != contract.ClientPort || current.peerPort != contract.PeerPort {
		return EtcdRecoveryObservation{}, errors.New("xRocket current etcd endpoint shape differs from the frozen rollback contract")
	}
	state := restoreEtcdState{
		ConfigPath: contract.ConfigPath, EtcdctlPath: contract.EtcdctlPath, Scheme: contract.Scheme,
		ClientAddress: current.client, ClientPort: contract.ClientPort,
		PeerAddress: current.peer, PeerPort: contract.PeerPort,
		MemberID: contract.MemberID, MemberCount: contract.MemberCount,
		ServiceName: contract.ServiceName, ControlAdapter: contract.ControlAdapter,
	}
	return inspector.observeEtcd(ctx, state)
}

func (inspector *productionReadOnlyInspector) CurrentBootID(ctx context.Context, _ RollbackOSContract) (string, error) {
	data, err := inspector.probe.execute(ctx, executor.Command{Name: "cat", Args: []string{"--", "/proc/sys/kernel/random/boot_id"}})
	if err != nil {
		return "", err
	}
	bootID := strings.TrimSpace(data)
	if bootID == "" || strings.ContainsAny(bootID, " \t\r\n") {
		return "", errors.New("xRocket boot_id observation is empty or malformed")
	}
	return bootID, nil
}

func (inspector *productionReadOnlyInspector) InspectRollback(ctx context.Context, expectation rollbackStageExpectation) (RollbackObservation, error) {
	if err := validateRollbackExpectation(expectation); err != nil {
		return RollbackObservation{}, err
	}
	observation := RollbackObservation{Satisfied: true}
	switch expectation.Kind {
	case stageKindSnapshot, stageKindFinal:
		return observation, nil
	case stageKindAlias:
		matched, err := inspector.artifactCurrentMatches(expectation.Alias.NetworkConfig)
		if err != nil || !matched {
			return RollbackObservation{Satisfied: false}, err
		}
		network, err := inspector.probe.observeNetwork(ctx)
		if err != nil {
			return RollbackObservation{}, err
		}
		return RollbackObservation{Satisfied: interfaceAddressesEqual(network, expectation.Alias.Interface, expectation.Alias.InterfaceAddresses)}, nil
	case stageKindProduct:
		for _, artifact := range []RecoveryArtifactRef{expectation.Product.ProductBundle, expectation.Product.KeepalivedConfig} {
			matched, err := inspector.artifactCurrentMatches(artifact)
			if err != nil || !matched {
				return RollbackObservation{Satisfied: false}, err
			}
		}
		return observation, nil
	case stageKindExternalDB:
		matched, err := inspector.artifactCurrentMatches(expectation.ExternalDB.BaselineConfig)
		return RollbackObservation{Satisfied: matched}, err
	case stageKindEtcd:
		matched, err := inspector.artifactCurrentMatches(expectation.Etcd.ConfigArtifact)
		if err != nil || !matched {
			return RollbackObservation{Satisfied: false}, err
		}
		etcdState, err := inspector.EtcdRecoveryState(ctx, *expectation.Etcd)
		if err != nil {
			return RollbackObservation{}, err
		}
		observation.Etcd = &etcdState
		observation.Satisfied = etcdState.Healthy && etcdState.MemberID == expectation.Etcd.MemberID && etcdState.MemberCount == 1 && etcdState.ClientAddress == expectation.Etcd.OldClient && etcdState.PeerAddress == expectation.Etcd.OldPeer && etcdState.LogicalKVDigest == expectation.Etcd.LogicalKVDigest
		return observation, nil
	case stageKindConfd:
		for _, artifact := range expectation.Confd.DestinationFiles {
			matched, err := inspector.artifactCurrentMatches(artifact)
			if err != nil || !matched {
				return RollbackObservation{Satisfied: false}, err
			}
		}
		return observation, nil
	case stageKindOS:
		matched, err := inspector.artifactCurrentMatches(expectation.OS.NetworkConfig)
		if err != nil || !matched {
			return RollbackObservation{Satisfied: false}, err
		}
		network, err := inspector.probe.observeNetwork(ctx)
		if err != nil {
			return RollbackObservation{}, err
		}
		observation.Satisfied = interfaceAddressesEqual(network, expectation.OS.Interface, expectation.OS.InterfaceAddresses) && networkHasDefaultRoute(network, expectation.OS.Interface, expectation.OS.Gateway)
		bootID, err := inspector.CurrentBootID(ctx, *expectation.OS)
		if err != nil {
			return RollbackObservation{}, err
		}
		observation.BootID = bootID
		return observation, nil
	default:
		return RollbackObservation{}, fmt.Errorf("unsupported xRocket rollback inspection kind %q", expectation.Kind)
	}
}

func (inspector *productionReadOnlyInspector) artifactCurrentMatches(artifact RecoveryArtifactRef) (bool, error) {
	if err := validateRecoveryRefPath(artifact); err != nil {
		return false, err
	}
	digest, err := hashFile(inspector.fs, artifact.BackupRef)
	if err != nil {
		return false, err
	}
	if digest != artifact.SHA256 {
		return false, nil
	}
	if artifact.Kind == recoveryKindEtcdSnapshot {
		return true, nil
	}
	backup, err := readRecoveryFileArtifact(inspector.fs, artifact.BackupRef)
	if err != nil {
		return false, err
	}
	for _, expected := range backup.Entries {
		current, err := readExactRecoverySource(inspector.fs, expected.SourcePath)
		if err != nil {
			return false, err
		}
		if !reflect.DeepEqual(current, expected) {
			return false, nil
		}
	}
	return true, nil
}

func readRecoveryFileArtifact(fs recoveryFilesystem, name string) (recoveryFileArtifact, error) {
	info, err := fs.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxRecoveryBundleBytes*2 {
		return recoveryFileArtifact{}, errors.New("xRocket recovery file artifact is missing or invalid")
	}
	resolved, err := fs.EvalSymlinks(name)
	if err != nil || resolved != name {
		return recoveryFileArtifact{}, errors.New("xRocket recovery file artifact contains path/symlink ambiguity")
	}
	file, err := fs.Open(name)
	if err != nil {
		return recoveryFileArtifact{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxRecoveryBundleBytes*2+1))
	decoder.DisallowUnknownFields()
	var artifact recoveryFileArtifact
	if err := decoder.Decode(&artifact); err != nil {
		return recoveryFileArtifact{}, errors.New("xRocket recovery file artifact encoding is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return recoveryFileArtifact{}, errors.New("xRocket recovery file artifact contains trailing data")
	}
	if artifact.SchemaVersion != recoveryFileArtifactSchema || len(artifact.Entries) == 0 {
		return recoveryFileArtifact{}, errors.New("xRocket recovery file artifact identity is invalid")
	}
	seen := map[string]struct{}{}
	for _, entry := range artifact.Entries {
		if !boundedAbsolutePath(entry.SourcePath) || entry.LinkType != "regular" || entry.Mode > 0o7777 || int64(len(entry.Data)) > maxRecoverySourceBytes {
			return recoveryFileArtifact{}, errors.New("xRocket recovery file entry is invalid")
		}
		if _, duplicate := seen[entry.SourcePath]; duplicate {
			return recoveryFileArtifact{}, errors.New("xRocket recovery file artifact contains duplicate source paths")
		}
		seen[entry.SourcePath] = struct{}{}
	}
	return artifact, nil
}

func (inspector *productionReadOnlyInspector) observeEtcd(ctx context.Context, state restoreEtcdState) (EtcdRecoveryObservation, error) {
	data, err := readObservationBytes(inspector.fs, state.ConfigPath)
	if err != nil {
		return EtcdRecoveryObservation{}, err
	}
	parsed, err := parseEtcdConfig(string(data), state.ClientAddress)
	if err != nil || parsed.Scheme != state.Scheme || parsed.ClientPort != state.ClientPort || parsed.PeerPort != state.PeerPort {
		return EtcdRecoveryObservation{}, errors.New("xRocket observed etcd config differs from the frozen contract")
	}
	endpoint := etcdEndpoint(state.Scheme, state.ClientAddress, state.ClientPort)
	health, err := inspector.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "endpoint", "health"}})
	if err != nil || health.StdoutTruncated || health.StderrTruncated || !strictEtcdHealthOutput(health.Stdout) {
		return EtcdRecoveryObservation{}, errors.New("xRocket observed etcd endpoint is not healthy")
	}
	members, err := inspector.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "member", "list", "-w", "json"}, OutputLimit: maxLogicalKVOutputBytes})
	if err != nil || members.StdoutTruncated || members.StderrTruncated {
		return EtcdRecoveryObservation{}, errors.New("xRocket observed etcd member list is unavailable")
	}
	memberID, memberCount, err := parseEtcdMemberList(members.Stdout, state.PeerAddress, state.PeerPort)
	if err != nil {
		return EtcdRecoveryObservation{}, err
	}
	kvResult, err := inspector.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", state.EtcdctlPath, "--endpoints=" + endpoint, "get", "", "--prefix", "-w", "json"}, OutputLimit: maxLogicalKVOutputBytes})
	if err != nil || kvResult.StdoutTruncated || kvResult.StderrTruncated {
		return EtcdRecoveryObservation{}, errors.New("xRocket observed etcd logical KV state is unavailable")
	}
	digest, err := deterministicKVDigest([]byte(kvResult.Stdout))
	if err != nil {
		return EtcdRecoveryObservation{}, err
	}
	return EtcdRecoveryObservation{MemberID: memberID, MemberCount: memberCount, ClientAddress: state.ClientAddress, PeerAddress: state.PeerAddress, Healthy: true, LogicalKVDigest: digest}, nil
}

func readObservationBytes(fs recoveryFilesystem, source string) ([]byte, error) {
	if !boundedAbsolutePath(source) || strings.HasPrefix(source, recoveryArtifactRoot+"/") || source == recoveryArtifactRoot {
		return nil, errors.New("xRocket read-only observation path is outside the bounded source envelope")
	}
	resolved, err := fs.EvalSymlinks(source)
	if err != nil || !boundedAbsolutePath(resolved) || strings.HasPrefix(resolved, recoveryArtifactRoot+"/") || resolved == recoveryArtifactRoot {
		return nil, errors.New("xRocket read-only observation path cannot be resolved safely")
	}
	info, err := fs.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxRecoverySourceBytes {
		return nil, errors.New("xRocket read-only observation source is not one bounded regular file")
	}
	file, err := fs.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecoverySourceBytes+1))
	if err != nil || int64(len(data)) > maxRecoverySourceBytes || int64(len(data)) != info.Size() {
		return nil, errors.New("xRocket read-only observation could not read an exact bounded file")
	}
	return data, nil
}

func networkHasAddress(observation networkObservation, interfaceName, address string, prefix int) bool {
	for _, device := range observation.addresses {
		if device.IfName != interfaceName {
			continue
		}
		for _, current := range device.AddrInfo {
			if current.Family == "inet" && current.Scope == "global" && canonicalIPv4(current.Local) == canonicalIPv4(address) && current.PrefixLen == prefix {
				return true
			}
		}
	}
	return false
}

func networkContainsAddress(observation networkObservation, address string) bool {
	return observation.containsAddress(address)
}

func networkHasDefaultRoute(observation networkObservation, interfaceName, gateway string) bool {
	for _, route := range observation.routes {
		if route.Dev == interfaceName && canonicalIPv4(route.Gateway) == canonicalIPv4(gateway) {
			return true
		}
	}
	return false
}

func interfaceAddressesEqual(observation networkObservation, interfaceName string, expected []restoreInterfaceAddress) bool {
	var current []restoreInterfaceAddress
	for _, device := range observation.addresses {
		if device.IfName != interfaceName {
			continue
		}
		for _, address := range device.AddrInfo {
			if address.Family == "inet" && address.Scope == "global" && canonicalIPv4(address.Local) != "" && address.PrefixLen >= 1 && address.PrefixLen <= 32 {
				current = append(current, restoreInterfaceAddress{Address: canonicalIPv4(address.Local), PrefixLength: address.PrefixLen})
			}
		}
	}
	sort.Slice(current, func(left, right int) bool {
		if current[left].Address != current[right].Address {
			return current[left].Address < current[right].Address
		}
		return current[left].PrefixLength < current[right].PrefixLength
	})
	wanted := append([]restoreInterfaceAddress(nil), expected...)
	sort.Slice(wanted, func(left, right int) bool {
		if wanted[left].Address != wanted[right].Address {
			return wanted[left].Address < wanted[right].Address
		}
		return wanted[left].PrefixLength < wanted[right].PrefixLength
	})
	return reflect.DeepEqual(current, wanted)
}
