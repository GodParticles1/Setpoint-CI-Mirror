//go:build linux

package xrocketreaddress

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const mutationCommandOutputLimit = 64 << 10

type mutationFilesystem interface {
	recoveryFilesystem
	Chown(string, int, int) error
}

func (osRecoveryFilesystem) Chown(name string, uid, gid int) error { return os.Chown(name, uid, gid) }

type productionMutationAdapter struct {
	executor executor.CommandExecutor
	fs       mutationFilesystem
	sleep    func(context.Context, time.Duration) error
}

func newProductionMutationAdapter(commandExecutor executor.CommandExecutor) (productionLocalAdapters, error) {
	return &productionMutationAdapter{
		executor: commandExecutor,
		fs:       osRecoveryFilesystem{},
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}, nil
}

func (adapter *productionMutationAdapter) AddTargetAlias(ctx context.Context, contract AliasStageContract) (LocalMutationReceipt, error) {
	base := []string{"alias", contract.NodeID, contract.Role, contract.Interface, contract.OldAddress, contract.NewAddress}
	if contract.NodeID == "" || !safeMutationName(contract.Interface) || canonicalIPv4(contract.OldAddress) == "" || canonicalIPv4(contract.NewAddress) == "" || contract.OldAddress == contract.NewAddress || contract.PrefixLength < 1 || contract.PrefixLength > 32 || canonicalIPv4(contract.Gateway) == "" {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket alias mutation contract is invalid")
	}
	network, err := (discoveryProbe{executor: adapter.executor}).observeNetwork(ctx)
	if err != nil {
		return mutationReceiptState(MutationNotStarted, base...), err
	}
	if !networkHasAddress(network, contract.Interface, contract.OldAddress, contract.PrefixLength) || !networkHasDefaultRoute(network, contract.Interface, contract.Gateway) || networkContainsAddress(network, contract.NewAddress) {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket alias live precondition drifted from the frozen old network state")
	}
	if err := adapter.run(ctx, executor.Command{Name: "ip", Args: []string{"address", "add", fmt.Sprintf("%s/%d", canonicalIPv4(contract.NewAddress), contract.PrefixLength), "dev", contract.Interface}}); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, base...), err
	}
	return mutationReceipt(base...), nil
}

func (adapter *productionMutationAdapter) ReaddressProduct(ctx context.Context, contract ProductStageContract) (LocalMutationReceipt, error) {
	if contract.InstallProfile != installProfileRootNative || contract.ProductUser != "root" || contract.ProductHome != "/root" || contract.ProductPrefix != "" || contract.XrocketBinary != "/opt/data/xrocket/xrocket.v2" {
		return mutationReceiptState(MutationNotStarted, "product", contract.NodeID, contract.Role), errors.New("xRocket production mutation permits only the frozen root-native C68 envelope")
	}
	for _, value := range []string{contract.OldMaster, contract.OldSlave, contract.OldVIP, contract.NewMaster, contract.NewSlave, contract.NewVIP} {
		if canonicalIPv4(value) == "" {
			return mutationReceiptState(MutationNotStarted, "product", contract.NodeID, contract.Role), errors.New("xRocket product mutation contains an invalid site address")
		}
	}
	if err := adapter.verifyProductBaselines(contract); err != nil {
		return mutationReceiptState(MutationNotStarted, "product", contract.NodeID, contract.Role), err
	}
	args := []string{
		"updateIP", "dual",
		"--oldM=" + contract.OldMaster,
		"--oldS=" + contract.OldSlave,
		"--oldV=" + contract.OldVIP,
		"--newM=" + contract.NewMaster,
		"--newS=" + contract.NewSlave,
		"--newV=" + contract.NewVIP,
	}
	if err := adapter.run(ctx, executor.Command{Name: contract.XrocketBinary, Args: args}); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, "product", contract.NodeID, contract.Role), errors.New("xRocket product readdress failed; partial completion cannot be excluded")
	}
	return mutationReceipt("product", contract.NodeID, contract.Role, contract.OldMaster, contract.OldSlave, contract.OldVIP, contract.NewMaster, contract.NewSlave, contract.NewVIP), nil
}

func (adapter *productionMutationAdapter) UpdateExternalDB(ctx context.Context, contract ExternalDBStageContract) (LocalMutationReceipt, error) {
	if !contract.PreserveNonAddressFields || !boundedAbsolutePath(contract.CommonYAMLPath) || canonicalIPv4(contract.OldAddress) == "" || canonicalIPv4(contract.NewAddress) == "" || contract.OldAddress == contract.NewAddress || contract.Port < 1 || contract.Port > 65535 {
		return mutationReceiptState(MutationNotStarted, "external-db", contract.NodeID, contract.Role), errors.New("xRocket external DB mutation contract is invalid")
	}
	artifact, err := adapter.loadRecoveryFile(contract.BaselineConfig, recoveryKindCommonYAML, contract.CommonYAMLPath)
	if err != nil {
		return mutationReceiptState(MutationNotStarted, "external-db", contract.NodeID, contract.Role), err
	}
	if len(artifact.Entries) != 1 {
		return mutationReceiptState(MutationNotStarted, "external-db", contract.NodeID, contract.Role), errors.New("xRocket external DB baseline cardinality is invalid")
	}
	current, err := readExactRecoverySource(adapter.fs, contract.CommonYAMLPath)
	if err != nil || !reflect.DeepEqual(current, artifact.Entries[0]) {
		return mutationReceiptState(MutationNotStarted, "external-db", contract.NodeID, contract.Role), errors.New("xRocket external DB source drifted from the run-owned baseline")
	}
	before, err := goldendbAddressOnlyObservation(string(artifact.Entries[0].Data))
	if err != nil || before.Address != canonicalIPv4(contract.OldAddress) || before.Port != contract.Port {
		return mutationReceiptState(MutationNotStarted, "external-db", contract.NodeID, contract.Role), errors.New("xRocket external DB baseline differs from the frozen endpoint")
	}
	updated := []byte(string(artifact.Entries[0].Data[:before.Start]) + canonicalIPv4(contract.NewAddress) + string(artifact.Entries[0].Data[before.End:]))
	ok, err := verifyExternalDBAddressOnlyTransition(artifact.Entries[0].Data, updated, contract.OldAddress, contract.NewAddress, contract.Port)
	if err != nil || !ok {
		return mutationReceiptState(MutationNotStarted, "external-db", contract.NodeID, contract.Role), errors.New("xRocket external DB address-only transition cannot be proven")
	}
	entry := artifact.Entries[0]
	entry.Data = updated
	if err := adapter.writeExact(entry); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, "external-db", contract.NodeID, contract.Role), fmt.Errorf("commit xRocket external DB mutation: %w", err)
	}
	return mutationReceipt("external-db", contract.NodeID, contract.Role, contract.CommonYAMLPath, contract.OldAddress, contract.NewAddress), nil
}

func (adapter *productionMutationAdapter) ReaddressEtcd(ctx context.Context, contract EtcdStageContract) (LocalMutationReceipt, error) {
	base := []string{"etcd", contract.NodeID, contract.Role, contract.MemberID, contract.OldClient, contract.NewClient}
	if contract.NodeID == "" || !boundedAbsolutePath(contract.ConfigPath) || !boundedAbsolutePath(contract.EtcdctlPath) || contract.MemberID == "" || contract.MemberCount != 1 || canonicalIPv4(contract.OldClient) == "" || canonicalIPv4(contract.NewClient) == "" || canonicalIPv4(contract.OldPeer) == "" || canonicalIPv4(contract.NewPeer) == "" || contract.ClientPort < 1 || contract.PeerPort < 1 || contract.ControlAdapter != "monit" || !safeMutationName(contract.ServiceName) || !validSHA256Digest(contract.LogicalKVDigest) {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket etcd mutation is outside the frozen singleton/Monit envelope")
	}
	entry, err := readExactRecoverySource(adapter.fs, contract.ConfigPath)
	if err != nil {
		return mutationReceiptState(MutationNotStarted, base...), err
	}
	current, err := parseMutationEtcdConfig(entry.Data)
	if err != nil || current.scheme != contract.Scheme || current.client != contract.OldClient || current.peer != contract.OldPeer || current.clientPort != contract.ClientPort || current.peerPort != contract.PeerPort {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket etcd source differs from the frozen old endpoint contract")
	}
	inspector := &productionReadOnlyInspector{executor: adapter.executor, fs: adapter.fs}
	observed, err := inspector.observeEtcd(ctx, restoreEtcdState{
		ConfigPath: contract.ConfigPath, EtcdctlPath: contract.EtcdctlPath, Scheme: contract.Scheme,
		ClientAddress: contract.OldClient, ClientPort: contract.ClientPort, PeerAddress: contract.OldPeer, PeerPort: contract.PeerPort,
		MemberID: contract.MemberID, MemberCount: contract.MemberCount, ServiceName: contract.ServiceName, ControlAdapter: contract.ControlAdapter,
	})
	if err != nil || !observed.Healthy || observed.MemberID != contract.MemberID || observed.MemberCount != 1 || observed.ClientAddress != contract.OldClient || observed.PeerAddress != contract.OldPeer || observed.LogicalKVDigest != contract.LogicalKVDigest {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket etcd live precondition drifted from the frozen singleton/KV state")
	}
	oldEndpoint := fmt.Sprintf("%s://%s:%d", contract.Scheme, contract.OldClient, contract.ClientPort)
	newPeerURL := fmt.Sprintf("%s://%s:%d", contract.Scheme, contract.NewPeer, contract.PeerPort)
	if err := adapter.run(ctx, mutationEtcdCommand(contract.EtcdctlPath, "--endpoints="+oldEndpoint, "member", "update", contract.MemberID, "--peer-urls="+newPeerURL)); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, base...), err
	}
	updated, err := mutateEtcdConfig(entry.Data, contract.OldClient, contract.NewClient, contract.OldPeer, contract.NewPeer)
	if err != nil {
		return mutationReceiptState(MutationChanged, base...), errors.New("xRocket etcd peer changed but config rewrite is not provable")
	}
	entry.Data = updated
	if err := adapter.writeExact(entry); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, base...), errors.New("xRocket etcd peer changed but config commit failed; partial completion requires rollback")
	}
	if err := adapter.run(ctx, executor.Command{Name: "monit", Args: []string{"restart", contract.ServiceName}}); err != nil {
		return mutationReceiptState(MutationChanged, base...), errors.New("xRocket etcd config changed but restart failed; partial completion requires rollback")
	}
	if err := adapter.waitEtcd(ctx, contract.EtcdctlPath, contract.Scheme, contract.NewClient, contract.ClientPort, contract.NewPeer, contract.PeerPort, contract.MemberID); err != nil {
		return mutationReceiptState(MutationChanged, base...), errors.New("xRocket etcd did not reach the frozen target identity; partial completion requires rollback")
	}
	return mutationReceipt(base...), nil
}

func (adapter *productionMutationAdapter) RenderConfd(ctx context.Context, contract ConfdStageContract) (LocalMutationReceipt, error) {
	if contract.InstallProfile != installProfileRootNative || contract.ProductUser != "root" || contract.ProductHome != "/root" || contract.ProductPrefix != "" || contract.XrocketBinary != "/opt/data/xrocket/xrocket.v2" {
		return mutationReceiptState(MutationNotStarted, "confd", contract.NodeID, contract.Role), errors.New("xRocket confd mutation permits only the frozen root-native C68 envelope")
	}
	if len(contract.Destinations) == 0 || len(contract.Destinations) != len(contract.DestinationFiles) {
		return mutationReceiptState(MutationNotStarted, "confd", contract.NodeID, contract.Role), errors.New("xRocket confd mutation requires one run-owned baseline per destination")
	}
	owner := ""
	for index, destination := range contract.Destinations {
		if contract.DestinationFiles[index].SourcePath != destination {
			return mutationReceiptState(MutationNotStarted, "confd", contract.NodeID, contract.Role), errors.New("xRocket confd baseline source correlation is invalid")
		}
		if err := sameMutationOwner(&owner, contract.DestinationFiles[index]); err != nil {
			return mutationReceiptState(MutationNotStarted, "confd", contract.NodeID, contract.Role), err
		}
		artifact, err := adapter.loadRecoveryFile(contract.DestinationFiles[index], recoveryKindConfdDestination, destination)
		if err != nil || len(artifact.Entries) != 1 {
			return mutationReceiptState(MutationNotStarted, "confd", contract.NodeID, contract.Role), errors.New("xRocket confd run-owned baseline is invalid")
		}
		current, err := readExactRecoverySource(adapter.fs, destination)
		if err != nil || !reflect.DeepEqual(current, artifact.Entries[0]) {
			return mutationReceiptState(MutationNotStarted, "confd", contract.NodeID, contract.Role), errors.New("xRocket confd destination drifted before mutation")
		}
	}
	if err := adapter.run(ctx, executor.Command{Name: contract.XrocketBinary, Args: []string{"confd"}}); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, "confd", contract.NodeID, contract.Role), errors.New("xRocket confd render failed; partial generated-config completion cannot be excluded")
	}
	return mutationReceipt("confd", contract.NodeID, contract.Role, strings.Join(contract.Destinations, ",")), nil
}

func (adapter *productionMutationAdapter) CutoverOS(ctx context.Context, contract OSStageContract) (LocalMutationReceipt, error) {
	base := []string{"os", contract.NodeID, contract.Role, contract.ConfigPath, contract.OldAddress, contract.NewAddress}
	if contract.NodeID == "" || !safeMutationName(contract.Interface) || !boundedAbsolutePath(contract.ConfigPath) || !strings.HasPrefix(contract.ConfigPath, "/etc/sysconfig/network-scripts/ifcfg-") || canonicalIPv4(contract.OldAddress) == "" || canonicalIPv4(contract.NewAddress) == "" || contract.OldAddress == contract.NewAddress || contract.PrefixLength < 1 || contract.PrefixLength > 32 || canonicalIPv4(contract.Gateway) == "" || contract.Barrier != operation.StageBarrierAgentReconnect || !contract.Reboot || strings.TrimSpace(contract.BootIDBefore) == "" {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket OS mutation is outside the frozen ifcfg/reboot envelope")
	}
	network, err := (discoveryProbe{executor: adapter.executor}).observeNetwork(ctx)
	if err != nil || !networkHasAddress(network, contract.Interface, contract.OldAddress, contract.PrefixLength) || !networkHasDefaultRoute(network, contract.Interface, contract.Gateway) {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket OS live network precondition drifted from the frozen old state")
	}
	bootID, err := adapter.bootID(ctx)
	if err != nil || bootID != contract.BootIDBefore {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket OS boot identity drifted before persistent mutation")
	}
	entry, err := readExactRecoverySource(adapter.fs, contract.ConfigPath)
	if err != nil {
		return mutationReceiptState(MutationNotStarted, base...), err
	}
	address, prefix, gateway, err := parseIfcfg(string(entry.Data))
	if err != nil || address != contract.OldAddress || prefix != contract.PrefixLength || gateway != contract.Gateway {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket ifcfg source differs from the frozen old address/prefix/gateway")
	}
	pattern := regexp.MustCompile("(?m)^([ \\t]*IPADDR(?:[0-9]+)?[ \\t]*=[ \\t]*[\"']?)" + regexp.QuoteMeta(contract.OldAddress) + "([\"']?[ \\t]*(?:#.*)?)$")
	if len(pattern.FindAllIndex(entry.Data, -1)) != 1 {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket ifcfg requires exactly one authoritative old IPADDR entry")
	}
	entry.Data = pattern.ReplaceAll(entry.Data, []byte("${1}"+contract.NewAddress+"${2}"))
	address, prefix, gateway, err = parseIfcfg(string(entry.Data))
	if err != nil || address != contract.NewAddress || prefix != contract.PrefixLength || gateway != contract.Gateway {
		return mutationReceiptState(MutationNotStarted, base...), errors.New("xRocket ifcfg address-only rewrite cannot be proven")
	}
	if err := adapter.writeExact(entry); err != nil {
		return mutationReceiptState(MutationMayHaveChanged, base...), err
	}
	return mutationReceipt(base...), nil
}

func (adapter *productionMutationAdapter) RestoreStage(ctx context.Context, expectation rollbackStageExpectation) (RollbackMutationReceipt, error) {
	base := []string{"rollback", string(expectation.Kind)}
	if err := validateRollbackExpectation(expectation); err != nil {
		return rollbackMutationReceiptState(MutationNotStarted, base...), err
	}
	if err := validateMutationOwnerSet(expectation); err != nil {
		return rollbackMutationReceiptState(MutationNotStarted, base...), err
	}
	switch expectation.Kind {
	case stageKindAlias:
		base = append(base, expectation.Alias.NodeID, expectation.Alias.Role)
		artifact, err := adapter.loadRecoveryFile(expectation.Alias.NetworkConfig, recoveryKindNetworkConfig, expectation.Alias.NetworkConfig.SourcePath)
		if err != nil || len(artifact.Entries) != 1 {
			return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket alias rollback baseline is invalid")
		}
		state, err := adapter.restoreEntries(artifact.Entries)
		if err != nil {
			return rollbackMutationReceiptState(state, base...), err
		}
		if err := adapter.restoreAddresses(ctx, expectation.Alias.Interface, expectation.Alias.InterfaceAddresses); err != nil {
			return rollbackMutationReceiptState(MutationMayHaveChanged, base...), err
		}
		return rollbackMutationReceiptFor(base...), nil
	case stageKindProduct:
		base = append(base, expectation.Product.NodeID, expectation.Product.Role)
		bundle, err := adapter.loadRecoveryFile(expectation.Product.ProductBundle, recoveryKindProductBundle, expectation.Product.ProductBundle.SourcePath)
		if err != nil {
			return rollbackMutationReceiptState(MutationNotStarted, base...), err
		}
		keepalived, err := adapter.loadRecoveryFile(expectation.Product.KeepalivedConfig, recoveryKindKeepalivedConfig, expectation.Product.KeepalivedConfig.SourcePath)
		if err != nil {
			return rollbackMutationReceiptState(MutationNotStarted, base...), err
		}
		entries := append(append([]recoveryFileEntry(nil), bundle.Entries...), keepalived.Entries...)
		state, err := adapter.restoreEntries(entries)
		if err != nil {
			return rollbackMutationReceiptState(state, base...), err
		}
		return rollbackMutationReceiptFor(base...), nil
	case stageKindExternalDB:
		base = append(base, expectation.ExternalDB.NodeID, expectation.ExternalDB.Role)
		artifact, err := adapter.loadRecoveryFile(expectation.ExternalDB.BaselineConfig, recoveryKindCommonYAML, expectation.ExternalDB.CommonYAMLPath)
		if err != nil || len(artifact.Entries) != 1 {
			return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket external DB rollback baseline is invalid")
		}
		state, err := adapter.restoreEntries(artifact.Entries)
		if err != nil {
			return rollbackMutationReceiptState(state, base...), err
		}
		return rollbackMutationReceiptFor(base...), nil
	case stageKindEtcd:
		return adapter.restoreEtcd(ctx, *expectation.Etcd)
	case stageKindConfd:
		base = append(base, expectation.Confd.NodeID, expectation.Confd.Role)
		var entries []recoveryFileEntry
		for index, destination := range expectation.Confd.Destinations {
			artifact, err := adapter.loadRecoveryFile(expectation.Confd.DestinationFiles[index], recoveryKindConfdDestination, destination)
			if err != nil || len(artifact.Entries) != 1 {
				return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket confd rollback baseline is invalid")
			}
			entries = append(entries, artifact.Entries[0])
		}
		state, err := adapter.restoreEntries(entries)
		if err != nil {
			return rollbackMutationReceiptState(state, base...), err
		}
		return rollbackMutationReceiptFor(base...), nil
	case stageKindOS:
		return adapter.restoreOS(ctx, *expectation.OS)
	default:
		return rollbackMutationReceiptState(MutationNotStarted, base...), fmt.Errorf("xRocket rollback kind %q is not mutable", expectation.Kind)
	}
}

func (adapter *productionMutationAdapter) restoreEtcd(ctx context.Context, contract RollbackEtcdContract) (RollbackMutationReceipt, error) {
	base := []string{"rollback", "etcd", contract.NodeID, contract.Role, contract.MemberID}
	config, err := adapter.loadRecoveryFile(contract.ConfigArtifact, recoveryKindEtcdConfig, contract.ConfigPath)
	if err != nil || len(config.Entries) != 1 {
		return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket etcd rollback config baseline is invalid")
	}
	if err := adapter.verifySnapshot(contract.SnapshotArtifact, contract.EtcdctlPath); err != nil {
		return rollbackMutationReceiptState(MutationNotStarted, base...), err
	}
	currentEntry, err := readExactRecoverySource(adapter.fs, contract.ConfigPath)
	if err != nil {
		return rollbackMutationReceiptState(MutationNotStarted, base...), err
	}
	current, err := parseMutationEtcdConfig(currentEntry.Data)
	if err != nil || current.scheme != contract.Scheme || current.clientPort != contract.ClientPort || current.peerPort != contract.PeerPort {
		return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket current etcd endpoint shape differs from the frozen rollback contract")
	}
	endpoint := fmt.Sprintf("%s://%s:%d", current.scheme, current.client, current.clientPort)
	oldPeerURL := fmt.Sprintf("%s://%s:%d", contract.Scheme, contract.OldPeer, contract.PeerPort)
	if err := adapter.run(ctx, mutationEtcdCommand(contract.EtcdctlPath, "--endpoints="+endpoint, "member", "update", contract.MemberID, "--peer-urls="+oldPeerURL)); err != nil {
		return rollbackMutationReceiptState(MutationMayHaveChanged, base...), err
	}
	if err := adapter.writeExact(config.Entries[0]); err != nil {
		return rollbackMutationReceiptState(MutationMayHaveChanged, base...), errors.New("xRocket etcd peer restored but config restore failed; partial completion is ambiguous")
	}
	if err := adapter.run(ctx, executor.Command{Name: "monit", Args: []string{"restart", contract.ServiceName}}); err != nil {
		return rollbackMutationReceiptState(MutationChanged, base...), errors.New("xRocket etcd config restored but restart failed; partial completion is ambiguous")
	}
	if err := adapter.waitEtcd(ctx, contract.EtcdctlPath, contract.Scheme, contract.OldClient, contract.ClientPort, contract.OldPeer, contract.PeerPort, contract.MemberID); err != nil {
		return rollbackMutationReceiptState(MutationChanged, base...), err
	}
	return rollbackMutationReceiptFor(base...), nil
}

func (adapter *productionMutationAdapter) restoreOS(ctx context.Context, contract RollbackOSContract) (RollbackMutationReceipt, error) {
	base := []string{"rollback", "os", contract.NodeID, contract.Role, contract.ConfigPath, contract.NetworkConfig.SHA256}
	if contract.Barrier != operation.StageBarrierAgentReconnect || !contract.Reboot {
		return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket OS rollback requires the reconnect/reboot barrier")
	}
	artifact, err := adapter.loadRecoveryFile(contract.NetworkConfig, recoveryKindNetworkConfig, contract.ConfigPath)
	if err != nil || len(artifact.Entries) != 1 {
		return rollbackMutationReceiptState(MutationNotStarted, base...), errors.New("xRocket OS rollback baseline is invalid")
	}
	bootBefore, err := adapter.bootID(ctx)
	if err != nil {
		return rollbackMutationReceiptState(MutationNotStarted, base...), err
	}
	state, err := adapter.restoreEntries(artifact.Entries)
	receipt := rollbackMutationReceiptState(state, base...)
	receipt.BootIDBeforeRollback = bootBefore
	if err != nil {
		return receipt, err
	}
	return receipt, nil
}

func (adapter *productionMutationAdapter) verifyProductBaselines(contract ProductStageContract) error {
	owner := ""
	for _, ref := range []RecoveryArtifactRef{contract.ProductBundle, contract.KeepalivedConfig} {
		if err := sameMutationOwner(&owner, ref); err != nil {
			return err
		}
	}
	bundle, err := adapter.loadRecoveryFile(contract.ProductBundle, recoveryKindProductBundle, contract.ProductBundle.SourcePath)
	if err != nil {
		return err
	}
	keepalived, err := adapter.loadRecoveryFile(contract.KeepalivedConfig, recoveryKindKeepalivedConfig, contract.KeepalivedConfig.SourcePath)
	if err != nil || len(keepalived.Entries) != 1 {
		return errors.New("xRocket product keepalived baseline is invalid")
	}
	for _, entry := range append(append([]recoveryFileEntry(nil), bundle.Entries...), keepalived.Entries...) {
		current, err := readExactRecoverySource(adapter.fs, entry.SourcePath)
		if err != nil || !reflect.DeepEqual(current, entry) {
			return errors.New("xRocket product controlled source drifted from the run-owned baseline")
		}
	}
	return nil
}

func (adapter *productionMutationAdapter) loadRecoveryFile(ref RecoveryArtifactRef, kind, source string) (recoveryFileArtifact, error) {
	if ref.Kind != kind || ref.SourcePath != source {
		return recoveryFileArtifact{}, errors.New("xRocket recovery artifact kind/source correlation is invalid")
	}
	if err := validateRecoveryRefPath(ref); err != nil {
		return recoveryFileArtifact{}, err
	}
	digest, err := hashFile(adapter.fs, ref.BackupRef)
	if err != nil || digest != ref.SHA256 {
		return recoveryFileArtifact{}, errors.New("xRocket recovery artifact SHA does not match accepted evidence")
	}
	return readRecoveryFileArtifact(adapter.fs, ref.BackupRef)
}

func (adapter *productionMutationAdapter) verifySnapshot(ref RecoveryArtifactRef, source string) error {
	if ref.Kind != recoveryKindEtcdSnapshot || ref.SourcePath != source {
		return errors.New("xRocket etcd snapshot correlation is invalid")
	}
	if err := validateRecoveryRefPath(ref); err != nil {
		return err
	}
	info, err := adapter.fs.Lstat(ref.BackupRef)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxEtcdSnapshotBytes {
		return errors.New("xRocket etcd snapshot artifact is invalid")
	}
	digest, err := hashFile(adapter.fs, ref.BackupRef)
	if err != nil || digest != ref.SHA256 {
		return errors.New("xRocket etcd snapshot SHA does not match accepted evidence")
	}
	return nil
}

func (adapter *productionMutationAdapter) restoreEntries(entries []recoveryFileEntry) (MutationState, error) {
	if len(entries) == 0 {
		return MutationNotStarted, errors.New("xRocket rollback recovery set is empty")
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if !boundedAbsolutePath(entry.SourcePath) || entry.LinkType != "regular" || int64(len(entry.Data)) > maxRecoverySourceBytes {
			return MutationNotStarted, errors.New("xRocket rollback recovery entry is invalid")
		}
		info, err := adapter.fs.Lstat(entry.SourcePath)
		if err != nil || !info.Mode().IsRegular() {
			return MutationNotStarted, errors.New("xRocket rollback destination is not an exact regular file")
		}
		resolved, err := adapter.fs.EvalSymlinks(entry.SourcePath)
		if err != nil || resolved != entry.SourcePath {
			return MutationNotStarted, errors.New("xRocket rollback destination contains symlink/path ambiguity")
		}
		if _, duplicate := seen[resolved]; duplicate {
			return MutationNotStarted, errors.New("xRocket rollback recovery set contains duplicate destination identity")
		}
		seen[resolved] = struct{}{}
		temp := entry.SourcePath + ".setpoint-xrocket.tmp"
		if _, err := adapter.fs.Lstat(temp); err == nil || !errors.Is(err, os.ErrNotExist) {
			return MutationNotStarted, errors.New("xRocket rollback destination temp path is not clean")
		}
	}
	for _, entry := range entries {
		if err := adapter.writeExact(entry); err != nil {
			return MutationMayHaveChanged, errors.New("xRocket rollback recovery set may be partially restored")
		}
	}
	return MutationChanged, nil
}

func (adapter *productionMutationAdapter) writeExact(entry recoveryFileEntry) error {
	info, err := adapter.fs.Lstat(entry.SourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("xRocket mutation destination is not an exact regular file")
	}
	resolved, err := adapter.fs.EvalSymlinks(entry.SourcePath)
	if err != nil || resolved != entry.SourcePath {
		return errors.New("xRocket mutation destination contains symlink/path ambiguity")
	}
	temp := entry.SourcePath + ".setpoint-xrocket.tmp"
	file, err := adapter.fs.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = adapter.fs.Remove(temp)
		}
	}()
	if _, err := file.Write(entry.Data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := adapter.fs.Chown(temp, int(entry.UID), int(entry.GID)); err != nil {
		return err
	}
	if err := adapter.fs.Chmod(temp, os.FileMode(entry.Mode)); err != nil {
		return err
	}
	if err := adapter.fs.Rename(temp, entry.SourcePath); err != nil {
		return err
	}
	committed = true
	if err := syncDirectory(adapter.fs, path.Dir(entry.SourcePath)); err != nil {
		return err
	}
	current, err := readExactRecoverySource(adapter.fs, entry.SourcePath)
	if err != nil || !reflect.DeepEqual(current, entry) {
		return errors.New("xRocket mutation destination could not be independently re-read")
	}
	return nil
}

func (adapter *productionMutationAdapter) restoreAddresses(ctx context.Context, interfaceName string, expected []restoreInterfaceAddress) error {
	if !safeMutationName(interfaceName) || len(expected) == 0 {
		return errors.New("xRocket rollback runtime-address contract is invalid")
	}
	network, err := (discoveryProbe{executor: adapter.executor}).observeNetwork(ctx)
	if err != nil {
		return err
	}
	current, err := restoreInterfaceAddresses(network, interfaceName)
	if err != nil {
		return err
	}
	key := func(value restoreInterfaceAddress) string { return fmt.Sprintf("%s/%d", value.Address, value.PrefixLength) }
	have, want := map[string]struct{}{}, map[string]struct{}{}
	for _, value := range current {
		have[key(value)] = struct{}{}
	}
	for _, value := range expected {
		want[key(value)] = struct{}{}
	}
	var add, del []string
	for value := range want {
		if _, ok := have[value]; !ok {
			add = append(add, value)
		}
	}
	for value := range have {
		if _, ok := want[value]; !ok {
			del = append(del, value)
		}
	}
	sort.Strings(add)
	sort.Strings(del)
	for _, value := range add {
		if err := adapter.run(ctx, executor.Command{Name: "ip", Args: []string{"address", "add", value, "dev", interfaceName}}); err != nil {
			return err
		}
	}
	for _, value := range del {
		if err := adapter.run(ctx, executor.Command{Name: "ip", Args: []string{"address", "del", value, "dev", interfaceName}}); err != nil {
			return errors.New("xRocket rollback runtime-address reconciliation is partially complete")
		}
	}
	return nil
}

type mutationEtcdState struct {
	scheme               string
	client, peer         string
	clientPort, peerPort int
}

func parseMutationEtcdConfig(data []byte) (mutationEtcdState, error) {
	values := parseKeyValueLines(string(data))
	scheme, client, clientPort, err := oneHTTPURL(values["ETCD_ADVERTISE_CLIENT_URLS"])
	if err != nil {
		return mutationEtcdState{}, err
	}
	peerScheme, peer, peerPort, err := oneHTTPURL(values["ETCD_INITIAL_ADVERTISE_PEER_URLS"])
	if err != nil || peerScheme != scheme {
		return mutationEtcdState{}, errors.New("xRocket etcd advertised endpoint shape is invalid")
	}
	return mutationEtcdState{scheme: scheme, client: client, peer: peer, clientPort: clientPort, peerPort: peerPort}, nil
}

func mutateEtcdConfig(data []byte, oldClient, newClient, oldPeer, newPeer string) ([]byte, error) {
	substitutions := []addressSubstitution{{Old: oldClient, New: newClient}}
	if oldPeer != oldClient || newPeer != newClient {
		substitutions = append(substitutions, addressSubstitution{Old: oldPeer, New: newPeer})
	}
	updated, counts, err := applyBoundedAddressSubstitutions(data, substitutions)
	if err != nil {
		return nil, err
	}
	for _, count := range counts {
		if count == 0 {
			return nil, errors.New("xRocket etcd config does not contain every frozen old endpoint")
		}
	}
	return updated, nil
}

func mutationEtcdCommand(etcdctlPath string, args ...string) executor.Command {
	values := []string{"ETCDCTL_API=3", etcdctlPath}
	values = append(values, args...)
	return executor.Command{Name: "env", Args: values}
}

func (adapter *productionMutationAdapter) waitEtcd(ctx context.Context, etcdctlPath, scheme, client string, clientPort int, peer string, peerPort int, memberID string) error {
	endpoint := fmt.Sprintf("%s://%s:%d", scheme, client, clientPort)
	for attempt := 0; attempt < 90; attempt++ {
		health, err := adapter.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", etcdctlPath, "--endpoints=" + endpoint, "endpoint", "health"}, OutputLimit: mutationCommandOutputLimit})
		if err == nil && !health.StdoutTruncated && !health.StderrTruncated && strictEtcdHealthOutput(health.Stdout) {
			members, memberErr := adapter.executor.Execute(ctx, executor.Command{Name: "env", Args: []string{"ETCDCTL_API=3", etcdctlPath, "--endpoints=" + endpoint, "member", "list", "-w", "json"}, OutputLimit: mutationCommandOutputLimit})
			if memberErr == nil && !members.StdoutTruncated && !members.StderrTruncated {
				id, count, parseErr := parseEtcdMemberList(members.Stdout, peer, peerPort)
				if parseErr == nil && id == memberID && count == 1 {
					return nil
				}
			}
		}
		if attempt+1 < 90 {
			if err := adapter.sleep(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}
	return errors.New("xRocket etcd target identity did not become healthy")
}

func (adapter *productionMutationAdapter) bootID(ctx context.Context) (string, error) {
	result, err := adapter.executor.Execute(ctx, executor.Command{Name: "cat", Args: []string{"--", "/proc/sys/kernel/random/boot_id"}, OutputLimit: mutationCommandOutputLimit})
	if err != nil || result.StdoutTruncated || result.StderrTruncated {
		return "", errors.New("xRocket boot_id observation failed")
	}
	value := strings.TrimSpace(result.Stdout)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("xRocket boot_id observation is malformed")
	}
	return value, nil
}

func (adapter *productionMutationAdapter) waitBoot(ctx context.Context, before string) (string, error) {
	for attempt := 0; attempt < 90; attempt++ {
		after, err := adapter.bootID(ctx)
		if err == nil && after != before {
			return after, nil
		}
		if attempt+1 < 90 {
			if err := adapter.sleep(ctx, 2*time.Second); err != nil {
				return "", err
			}
		}
	}
	return "", errors.New("xRocket reboot did not produce a boot_id transition")
}

func (adapter *productionMutationAdapter) run(ctx context.Context, command executor.Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command.OutputLimit = mutationCommandOutputLimit
	result, err := adapter.executor.Execute(ctx, command)
	if err != nil {
		return err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return errors.New("xRocket mutation command output exceeded the bounded limit")
	}
	return ctx.Err()
}

func sameMutationOwner(owner *string, ref RecoveryArtifactRef) error {
	if err := validateRecoveryRefPath(ref); err != nil {
		return err
	}
	if *owner == "" {
		*owner = ref.OwnerID
		return nil
	}
	if ref.OwnerID != *owner {
		return errors.New("xRocket recovery artifacts belong to different run-owned roots")
	}
	return nil
}

func validateMutationOwnerSet(expectation rollbackStageExpectation) error {
	owner := ""
	for _, ref := range rollbackRecoveryArtifacts(expectation) {
		if err := sameMutationOwner(&owner, ref); err != nil {
			return err
		}
	}
	if owner == "" {
		return errors.New("xRocket rollback mutation requires run-owned recovery artifacts")
	}
	return nil
}

func safeMutationName(value string) bool {
	return regexp.MustCompile("^[A-Za-z0-9_.:-]{1,64}$").MatchString(value)
}

func mutationReceiptState(state MutationState, parts ...string) LocalMutationReceipt {
	return LocalMutationReceipt{Digest: digestBytes([]byte("xrocket-production-mutation-v1|" + string(state) + "|" + strings.Join(parts, "|"))), State: state}
}

func mutationReceipt(parts ...string) LocalMutationReceipt {
	return mutationReceiptState(MutationChanged, parts...)
}

func rollbackMutationReceiptState(state MutationState, parts ...string) RollbackMutationReceipt {
	return RollbackMutationReceipt{Digest: digestBytes([]byte("xrocket-production-rollback-v1|" + string(state) + "|" + strings.Join(parts, "|"))), State: state}
}

func rollbackMutationReceiptFor(parts ...string) RollbackMutationReceipt {
	return rollbackMutationReceiptState(MutationChanged, parts...)
}

var _ LocalMutationAdapter = (*productionMutationAdapter)(nil)
var _ LocalRollbackMutationAdapter = (*productionMutationAdapter)(nil)
