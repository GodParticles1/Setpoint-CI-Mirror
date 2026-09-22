package xrocketreaddress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"setpoint/internal/operation"
)

const (
	RestorePointRollbackSchema       = "xrocket.readdress.restore-point.v2"
	RecoveryArtifactContractSchema   = "xrocket.readdress.recovery-artifacts.v1"
	RollbackResultSchema             = "xrocket.readdress.rollback-stage.v1"
	recoveryArtifactRoot             = "/var/lib/setpoint/recovery/xrocket"
	recoveryKindNetworkConfig        = "network_config"
	recoveryKindProductBundle        = "product_config_bundle"
	recoveryKindKeepalivedConfig     = "keepalived_config"
	recoveryKindCommonYAML           = "common_yaml"
	recoveryKindEtcdConfig           = "etcd_config"
	recoveryKindEtcdSnapshot         = "etcd_snapshot"
	recoveryKindConfdDestination     = "confd_destination"
	rollbackPostconditionFindingCode = "XROCKET_ROLLBACK_POSTCONDITION_MISMATCH"
)

type RecoveryArtifactOwner struct {
	OwnerID            string   `json:"owner_id"`
	RunID              string   `json:"run_id"`
	NodeID             string   `json:"node_id"`
	StageID            string   `json:"stage_id"`
	StageIndex         int      `json:"stage_index"`
	Role               string   `json:"role"`
	ParticipantNodeIDs []string `json:"participant_node_ids"`
}

type RecoveryArtifactRef struct {
	ID         string `json:"id"`
	OwnerID    string `json:"owner_id"`
	Kind       string `json:"kind"`
	SourcePath string `json:"source_path"`
	BackupRef  string `json:"backup_ref"`
	SHA256     string `json:"sha256"`
}

type RecoveryArtifactContract struct {
	SchemaVersion   string                `json:"schema_version"`
	Owner           RecoveryArtifactOwner `json:"owner"`
	Artifacts       []RecoveryArtifactRef `json:"artifacts,omitempty"`
	LogicalKVDigest string                `json:"logical_kv_digest,omitempty"`
}

type RecoveryArtifactCaptureRequest struct {
	Owner     RecoveryArtifactOwner
	StageKind string
	Before    restoreBeforeState
}

type RecoveryArtifactCollector interface {
	Capture(context.Context, RecoveryArtifactCaptureRequest) (RecoveryArtifactContract, error)
}

type LocalRollbackMutationAdapter interface {
	RestoreStage(context.Context, rollbackStageExpectation) (RollbackMutationReceipt, error)
}

type EtcdRecoveryObservation struct {
	MemberID        string
	MemberCount     int
	ClientAddress   string
	PeerAddress     string
	Healthy         bool
	LogicalKVDigest string
}

type RollbackObservation struct {
	Satisfied bool
	BootID    string
	Etcd      *EtcdRecoveryObservation
}

type LocalRollbackInspectionAdapter interface {
	RecoveryArtifactMatches(context.Context, RecoveryArtifactRef) (bool, error)
	EtcdRecoveryState(context.Context, RollbackEtcdContract) (EtcdRecoveryObservation, error)
	CurrentBootID(context.Context, RollbackOSContract) (string, error)
	InspectRollback(context.Context, rollbackStageExpectation) (RollbackObservation, error)
}

type RollbackMutationReceipt struct {
	Digest               string        `json:"digest"`
	State                MutationState `json:"state"`
	BootIDBeforeRollback string        `json:"boot_id_before_rollback,omitempty"`
}

type RollbackAliasContract struct {
	NodeID             string                    `json:"node_id"`
	Role               string                    `json:"role"`
	Interface          string                    `json:"interface"`
	InterfaceAddresses []restoreInterfaceAddress `json:"interface_addresses"`
	NetworkConfig      RecoveryArtifactRef       `json:"network_config"`
}

type RollbackProductContract struct {
	NodeID              string              `json:"node_id"`
	Role                string              `json:"role"`
	OldMaster           string              `json:"old_master"`
	OldSlave            string              `json:"old_slave"`
	OldVIP              string              `json:"old_vip"`
	ProductBundle       RecoveryArtifactRef `json:"product_bundle"`
	KeepalivedConfig    RecoveryArtifactRef `json:"keepalived_config"`
	KeepalivedSource    string              `json:"keepalived_source"`
	KeepalivedPeer      string              `json:"keepalived_peer"`
	KeepalivedPriority  int                 `json:"keepalived_priority"`
	KeepalivedNopreempt bool                `json:"keepalived_nopreempt"`
}

type RollbackExternalDBContract struct {
	NodeID         string              `json:"node_id"`
	Role           string              `json:"role"`
	CommonYAMLPath string              `json:"common_yaml_path"`
	OldAddress     string              `json:"old_address"`
	Port           int                 `json:"port"`
	AddressOnly    bool                `json:"address_only"`
	BaselineConfig RecoveryArtifactRef `json:"baseline_config"`
}

type RollbackEtcdContract struct {
	NodeID           string              `json:"node_id"`
	Role             string              `json:"role"`
	ConfigPath       string              `json:"config_path"`
	EtcdctlPath      string              `json:"etcdctl_path"`
	Scheme           string              `json:"scheme"`
	MemberID         string              `json:"member_id"`
	MemberCount      int                 `json:"member_count"`
	OldClient        string              `json:"old_client_address"`
	ClientPort       int                 `json:"client_port"`
	OldPeer          string              `json:"old_peer_address"`
	PeerPort         int                 `json:"peer_port"`
	ServiceName      string              `json:"service_name"`
	ControlAdapter   string              `json:"control_adapter"`
	ConfigArtifact   RecoveryArtifactRef `json:"config_artifact"`
	SnapshotArtifact RecoveryArtifactRef `json:"snapshot_artifact"`
	LogicalKVDigest  string              `json:"logical_kv_digest"`
}

type RollbackConfdContract struct {
	NodeID           string                `json:"node_id"`
	Role             string                `json:"role"`
	Destinations     []string              `json:"destinations"`
	DestinationFiles []RecoveryArtifactRef `json:"destination_files"`
	OldMaster        string                `json:"old_master"`
	OldSlave         string                `json:"old_slave"`
	OldVIP           string                `json:"old_vip"`
	OldExternalDB    string                `json:"old_external_db"`
}

type RollbackOSContract struct {
	NodeID             string                    `json:"node_id"`
	Role               string                    `json:"role"`
	Interface          string                    `json:"interface"`
	ConfigPath         string                    `json:"config_path"`
	OldAddress         string                    `json:"old_address"`
	PrefixLength       int                       `json:"prefix_length"`
	Gateway            string                    `json:"gateway"`
	InterfaceAddresses []restoreInterfaceAddress `json:"interface_addresses"`
	NetworkConfig      RecoveryArtifactRef       `json:"network_config"`
	OriginalBootID     string                    `json:"original_boot_id"`
	Barrier            operation.StageBarrier    `json:"barrier"`
	Reboot             bool                      `json:"reboot"`
}

type rollbackStageExpectation struct {
	Kind       stageKind                   `json:"kind"`
	Alias      *RollbackAliasContract      `json:"alias,omitempty"`
	Product    *RollbackProductContract    `json:"product,omitempty"`
	ExternalDB *RollbackExternalDBContract `json:"external_db,omitempty"`
	Etcd       *RollbackEtcdContract       `json:"etcd,omitempty"`
	Confd      *RollbackConfdContract      `json:"confd,omitempty"`
	OS         *RollbackOSContract         `json:"os,omitempty"`
}

type rollbackStageReceipt struct {
	SchemaVersion          string                   `json:"schema_version"`
	OperationID            string                   `json:"operation_id"`
	OperationVersion       string                   `json:"operation_version"`
	RunID                  string                   `json:"run_id"`
	StageID                string                   `json:"stage_id"`
	StageIndex             int                      `json:"stage_index"`
	NodeID                 string                   `json:"node_id"`
	ParticipantNodeIDs     []string                 `json:"participant_node_ids"`
	Role                   string                   `json:"role"`
	Checkpoint             string                   `json:"checkpoint"`
	Barrier                operation.StageBarrier   `json:"barrier,omitempty"`
	RestorePointID         string                   `json:"restore_point_id"`
	RestoreManifestSHA256  string                   `json:"restore_manifest_sha256"`
	RecoveryContractSHA256 string                   `json:"recovery_contract_sha256"`
	ApplyStateSHA256       string                   `json:"apply_state_sha256"`
	ApplyChanged           bool                     `json:"apply_changed"`
	MutationPerformed      bool                     `json:"mutation_performed"`
	Expectation            rollbackStageExpectation `json:"expectation"`
	AdapterReceipt         *RollbackMutationReceipt `json:"adapter_receipt,omitempty"`
	Failed                 bool                     `json:"failed,omitempty"`
}

func recoveryOwnerFromManifest(manifest restorePointManifest) RecoveryArtifactOwner {
	return RecoveryArtifactOwner{
		OwnerID:            recoveryOwnerID(manifest),
		RunID:              manifest.RunID,
		NodeID:             manifest.NodeID,
		StageID:            manifest.StageID,
		StageIndex:         manifest.StageIndex,
		Role:               manifest.Role,
		ParticipantNodeIDs: append([]string(nil), manifest.ParticipantNodeIDs...),
	}
}

func recoveryOwnerID(manifest restorePointManifest) string {
	return digestBytes([]byte(strings.Join([]string{
		OperationID,
		Metadata().Version,
		manifest.RunID,
		manifest.NodeID,
		manifest.StageID,
		fmt.Sprintf("%d", manifest.StageIndex),
		manifest.Role,
		strings.Join(manifest.ParticipantNodeIDs, ","),
	}, "|")))
}

func recoveryBackupRoot(ownerID string) string {
	value := strings.TrimPrefix(ownerID, "sha256:")
	if len(value) > 32 {
		value = value[:32]
	}
	return recoveryArtifactRoot + "/" + value
}

func recoveryArtifactID(value RecoveryArtifactRef) string {
	return digestBytes([]byte(strings.Join([]string{value.OwnerID, value.Kind, value.SourcePath, value.BackupRef, value.SHA256}, "|")))
}

func validateRecoveryArtifact(ref RecoveryArtifactRef, owner RecoveryArtifactOwner) error {
	if ref.OwnerID != owner.OwnerID || !validSHA256Digest(ref.OwnerID) {
		return errors.New("xRocket recovery artifact owner identity is invalid")
	}
	if strings.TrimSpace(ref.Kind) == "" || !boundedAbsolutePath(ref.SourcePath) || !boundedAbsolutePath(ref.BackupRef) || !validSHA256Digest(ref.SHA256) {
		return errors.New("xRocket recovery artifact path/hash contract is incomplete")
	}
	root := recoveryBackupRoot(owner.OwnerID) + "/"
	if !strings.HasPrefix(ref.BackupRef, root) {
		return errors.New("xRocket recovery artifact is outside the run-owned recovery root")
	}
	if ref.ID != recoveryArtifactID(ref) || !validSHA256Digest(ref.ID) {
		return errors.New("xRocket recovery artifact identity digest is invalid")
	}
	return nil
}

func validateRecoveryContract(manifest restorePointManifest) error {
	if manifest.Recovery == nil {
		return errors.New("xRocket rollback restore manifest is missing recovery artifacts")
	}
	recovery := *manifest.Recovery
	owner := recoveryOwnerFromManifest(manifest)
	if recovery.SchemaVersion != RecoveryArtifactContractSchema || !reflect.DeepEqual(recovery.Owner, owner) {
		return errors.New("xRocket recovery artifact ownership does not match the restore manifest")
	}
	artifacts := append([]RecoveryArtifactRef(nil), recovery.Artifacts...)
	sort.Slice(artifacts, func(left, right int) bool {
		if artifacts[left].Kind != artifacts[right].Kind {
			return artifacts[left].Kind < artifacts[right].Kind
		}
		return artifacts[left].SourcePath < artifacts[right].SourcePath
	})
	if !reflect.DeepEqual(artifacts, recovery.Artifacts) {
		return errors.New("xRocket recovery artifacts must be canonicalized")
	}
	seen := map[string]struct{}{}
	for _, artifact := range recovery.Artifacts {
		if err := validateRecoveryArtifact(artifact, owner); err != nil {
			return err
		}
		key := artifact.Kind + "|" + artifact.SourcePath
		if _, exists := seen[key]; exists {
			return errors.New("xRocket recovery artifact contract contains duplicates")
		}
		seen[key] = struct{}{}
	}
	if manifest.StageIndex < 0 || manifest.StageIndex >= len(canonicalStages) {
		return errors.New("xRocket recovery artifact stage index is invalid")
	}
	spec := canonicalStages[manifest.StageIndex]
	if spec.ID != manifest.StageID || spec.Role != manifest.Role {
		return errors.New("xRocket recovery artifact stage identity is invalid")
	}
	if err := validateRecoveryArtifactsForStage(manifest, spec); err != nil {
		return err
	}
	return nil
}

func validateRecoveryArtifactsForStage(manifest restorePointManifest, spec canonicalStage) error {
	recovery := manifest.Recovery
	before := manifest.Before
	require := func(kind, source string) error {
		_, err := recoveryArtifactByKindSource(*recovery, kind, source)
		return err
	}
	switch spec.Kind {
	case stageKindSnapshot, stageKindFinal:
		if len(recovery.Artifacts) != 0 || recovery.LogicalKVDigest != "" {
			return errors.New("xRocket read-only rollback stage cannot carry recovery artifacts")
		}
	case stageKindAlias:
		if len(recovery.Artifacts) != 1 || recovery.LogicalKVDigest != "" {
			return errors.New("xRocket alias rollback requires exactly one network recovery artifact")
		}
		return require(recoveryKindNetworkConfig, before.Network.ConfigPath)
	case stageKindProduct:
		if len(recovery.Artifacts) != 2 || recovery.LogicalKVDigest != "" {
			return errors.New("xRocket product rollback requires product and keepalived recovery artifacts")
		}
		if err := require(recoveryKindProductBundle, before.Product.VersionEvidence); err != nil {
			return err
		}
		return require(recoveryKindKeepalivedConfig, before.HA.ConfigPath)
	case stageKindExternalDB:
		if len(recovery.Artifacts) != 1 || recovery.LogicalKVDigest != "" {
			return errors.New("xRocket external DB rollback requires exactly one common.yaml recovery artifact")
		}
		return require(recoveryKindCommonYAML, before.Product.CommonYAMLPath)
	case stageKindEtcd:
		if len(recovery.Artifacts) != 2 || !validSHA256Digest(recovery.LogicalKVDigest) {
			return errors.New("xRocket etcd rollback requires config, snapshot and logical KV digest evidence")
		}
		if err := require(recoveryKindEtcdConfig, before.Etcd.ConfigPath); err != nil {
			return err
		}
		return require(recoveryKindEtcdSnapshot, before.Etcd.EtcdctlPath)
	case stageKindConfd:
		if len(recovery.Artifacts) != len(before.Rendering.ConfdDestinations) || recovery.LogicalKVDigest != "" {
			return errors.New("xRocket confd rollback recovery artifact cardinality is invalid")
		}
		for _, destination := range before.Rendering.ConfdDestinations {
			if err := require(recoveryKindConfdDestination, destination); err != nil {
				return err
			}
		}
	case stageKindOS:
		if len(recovery.Artifacts) != 1 || recovery.LogicalKVDigest != "" {
			return errors.New("xRocket OS rollback requires exactly one persistent network recovery artifact")
		}
		return require(recoveryKindNetworkConfig, before.Network.ConfigPath)
	default:
		return fmt.Errorf("unsupported xRocket rollback recovery stage %q", spec.Kind)
	}
	return nil
}

func recoveryArtifactByKindSource(contract RecoveryArtifactContract, kind, source string) (RecoveryArtifactRef, error) {
	matches := make([]RecoveryArtifactRef, 0, 1)
	for _, artifact := range contract.Artifacts {
		if artifact.Kind == kind && artifact.SourcePath == source {
			matches = append(matches, artifact)
		}
	}
	if len(matches) != 1 {
		return RecoveryArtifactRef{}, fmt.Errorf("xRocket recovery artifact %s for %s must resolve exactly once", kind, source)
	}
	return matches[0], nil
}

func recoveryContractDigest(contract RecoveryArtifactContract) (string, error) {
	payload, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return digestBytes(payload), nil
}

func buildRollbackExpectation(manifest restorePointManifest, spec canonicalStage) (rollbackStageExpectation, error) {
	if manifest.Recovery == nil {
		return rollbackStageExpectation{}, errors.New("xRocket rollback requires a recovery-artifact restore manifest")
	}
	before := manifest.Before
	recovery := *manifest.Recovery
	expectation := rollbackStageExpectation{Kind: spec.Kind}
	artifact := func(kind, source string) (RecoveryArtifactRef, error) {
		return recoveryArtifactByKindSource(recovery, kind, source)
	}
	switch spec.Kind {
	case stageKindSnapshot, stageKindFinal:
		return expectation, nil
	case stageKindAlias:
		networkConfig, err := artifact(recoveryKindNetworkConfig, before.Network.ConfigPath)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		expectation.Alias = &RollbackAliasContract{NodeID: manifest.NodeID, Role: spec.Role, Interface: before.Network.Interface, InterfaceAddresses: append([]restoreInterfaceAddress(nil), before.Network.InterfaceAddresses...), NetworkConfig: networkConfig}
	case stageKindProduct:
		productBundle, err := artifact(recoveryKindProductBundle, before.Product.VersionEvidence)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		keepalived, err := artifact(recoveryKindKeepalivedConfig, before.HA.ConfigPath)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		expectation.Product = &RollbackProductContract{NodeID: manifest.NodeID, Role: spec.Role, OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, ProductBundle: productBundle, KeepalivedConfig: keepalived, KeepalivedSource: before.HA.SourceAddress, KeepalivedPeer: before.HA.PeerAddress, KeepalivedPriority: before.HA.Priority, KeepalivedNopreempt: before.HA.Nopreempt}
	case stageKindExternalDB:
		config, err := artifact(recoveryKindCommonYAML, before.Product.CommonYAMLPath)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		expectation.ExternalDB = &RollbackExternalDBContract{NodeID: manifest.NodeID, Role: spec.Role, CommonYAMLPath: before.Product.CommonYAMLPath, OldAddress: before.Database.Address, Port: before.Database.Port, AddressOnly: true, BaselineConfig: config}
	case stageKindEtcd:
		config, err := artifact(recoveryKindEtcdConfig, before.Etcd.ConfigPath)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		snapshot, err := artifact(recoveryKindEtcdSnapshot, before.Etcd.EtcdctlPath)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		expectation.Etcd = &RollbackEtcdContract{NodeID: manifest.NodeID, Role: spec.Role, ConfigPath: before.Etcd.ConfigPath, EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme, MemberID: before.Etcd.MemberID, MemberCount: before.Etcd.MemberCount, OldClient: before.Etcd.ClientAddress, ClientPort: before.Etcd.ClientPort, OldPeer: before.Etcd.PeerAddress, PeerPort: before.Etcd.PeerPort, ServiceName: before.Etcd.ServiceName, ControlAdapter: before.Etcd.ControlAdapter, ConfigArtifact: config, SnapshotArtifact: snapshot, LogicalKVDigest: recovery.LogicalKVDigest}
	case stageKindConfd:
		destinations := append([]string(nil), before.Rendering.ConfdDestinations...)
		sort.Strings(destinations)
		files := make([]RecoveryArtifactRef, 0, len(destinations))
		for _, destination := range destinations {
			value, err := artifact(recoveryKindConfdDestination, destination)
			if err != nil {
				return rollbackStageExpectation{}, err
			}
			files = append(files, value)
		}
		expectation.Confd = &RollbackConfdContract{NodeID: manifest.NodeID, Role: spec.Role, Destinations: destinations, DestinationFiles: files, OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, OldExternalDB: before.Database.Address}
	case stageKindOS:
		networkConfig, err := artifact(recoveryKindNetworkConfig, before.Network.ConfigPath)
		if err != nil {
			return rollbackStageExpectation{}, err
		}
		expectation.OS = &RollbackOSContract{NodeID: manifest.NodeID, Role: spec.Role, Interface: before.Network.Interface, ConfigPath: before.Network.ConfigPath, OldAddress: before.Network.PersistentAddress, PrefixLength: before.Network.PersistentPrefix, Gateway: before.Network.PersistentGateway, InterfaceAddresses: append([]restoreInterfaceAddress(nil), before.Network.InterfaceAddresses...), NetworkConfig: networkConfig, OriginalBootID: before.Rendering.BootID, Barrier: operation.StageBarrierAgentReconnect, Reboot: true}
	default:
		return rollbackStageExpectation{}, fmt.Errorf("unsupported xRocket rollback stage kind %q", spec.Kind)
	}
	if err := validateRollbackExpectation(expectation); err != nil {
		return rollbackStageExpectation{}, err
	}
	return expectation, nil
}

func validateRollbackExpectation(expectation rollbackStageExpectation) error {
	set := 0
	if expectation.Alias != nil {
		set++
	}
	if expectation.Product != nil {
		set++
	}
	if expectation.ExternalDB != nil {
		set++
	}
	if expectation.Etcd != nil {
		set++
	}
	if expectation.Confd != nil {
		set++
	}
	if expectation.OS != nil {
		set++
	}
	if expectation.Kind == stageKindSnapshot || expectation.Kind == stageKindFinal {
		if set != 0 {
			return errors.New("xRocket read-only rollback stage cannot carry a mutation contract")
		}
		return nil
	}
	if set != 1 {
		return errors.New("xRocket rollback expectation must contain exactly one typed contract")
	}
	switch expectation.Kind {
	case stageKindAlias:
		if expectation.Alias == nil || expectation.Alias.NodeID == "" || expectation.Alias.Interface == "" || len(expectation.Alias.InterfaceAddresses) == 0 {
			return errors.New("xRocket alias rollback contract is incomplete")
		}
	case stageKindProduct:
		if expectation.Product == nil || expectation.Product.NodeID == "" || expectation.Product.OldMaster == "" || expectation.Product.OldSlave == "" || expectation.Product.OldVIP == "" || expectation.Product.KeepalivedSource == "" || expectation.Product.KeepalivedPeer == "" || expectation.Product.KeepalivedPriority <= 0 || !expectation.Product.KeepalivedNopreempt {
			return errors.New("xRocket product rollback contract is incomplete")
		}
	case stageKindExternalDB:
		if expectation.ExternalDB == nil || expectation.ExternalDB.NodeID == "" || !boundedAbsolutePath(expectation.ExternalDB.CommonYAMLPath) || expectation.ExternalDB.OldAddress == "" || expectation.ExternalDB.Port <= 0 || !expectation.ExternalDB.AddressOnly {
			return errors.New("xRocket external DB rollback contract is incomplete or not address-only")
		}
	case stageKindEtcd:
		if expectation.Etcd == nil || expectation.Etcd.NodeID == "" || !boundedAbsolutePath(expectation.Etcd.ConfigPath) || !boundedAbsolutePath(expectation.Etcd.EtcdctlPath) || expectation.Etcd.MemberID == "" || expectation.Etcd.MemberCount != 1 || expectation.Etcd.OldClient == "" || expectation.Etcd.OldPeer == "" || expectation.Etcd.ClientPort <= 0 || expectation.Etcd.PeerPort <= 0 || expectation.Etcd.ServiceName == "" || expectation.Etcd.ControlAdapter == "" || !validSHA256Digest(expectation.Etcd.LogicalKVDigest) {
			return errors.New("xRocket etcd rollback contract is incomplete")
		}
	case stageKindConfd:
		if expectation.Confd == nil || expectation.Confd.NodeID == "" || len(expectation.Confd.Destinations) == 0 || len(expectation.Confd.Destinations) != len(expectation.Confd.DestinationFiles) || expectation.Confd.OldMaster == "" || expectation.Confd.OldSlave == "" || expectation.Confd.OldVIP == "" || expectation.Confd.OldExternalDB == "" {
			return errors.New("xRocket confd rollback contract is incomplete")
		}
	case stageKindOS:
		if expectation.OS == nil || expectation.OS.NodeID == "" || expectation.OS.Interface == "" || !boundedAbsolutePath(expectation.OS.ConfigPath) || expectation.OS.OldAddress == "" || expectation.OS.PrefixLength <= 0 || expectation.OS.Gateway == "" || len(expectation.OS.InterfaceAddresses) == 0 || expectation.OS.OriginalBootID == "" || expectation.OS.Barrier != operation.StageBarrierAgentReconnect || !expectation.OS.Reboot {
			return errors.New("xRocket OS rollback contract is incomplete or lacks the reconnect barrier")
		}
	default:
		return fmt.Errorf("unsupported xRocket rollback expectation kind %q", expectation.Kind)
	}
	return nil
}

func rollbackRecoveryArtifacts(expectation rollbackStageExpectation) []RecoveryArtifactRef {
	var values []RecoveryArtifactRef
	switch expectation.Kind {
	case stageKindAlias:
		values = append(values, expectation.Alias.NetworkConfig)
	case stageKindProduct:
		values = append(values, expectation.Product.ProductBundle, expectation.Product.KeepalivedConfig)
	case stageKindExternalDB:
		values = append(values, expectation.ExternalDB.BaselineConfig)
	case stageKindEtcd:
		values = append(values, expectation.Etcd.ConfigArtifact, expectation.Etcd.SnapshotArtifact)
	case stageKindConfd:
		values = append(values, expectation.Confd.DestinationFiles...)
	case stageKindOS:
		values = append(values, expectation.OS.NetworkConfig)
	}
	return values
}

func (definition *Definition) rollbackStage(ctx context.Context, input operation.RollbackInput) (operation.RollbackResult, error) {
	stageContext, applyReceipt, expectation, err := definition.validateRollbackInput(input)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	mutationPerformed := false
	var adapterReceipt *RollbackMutationReceipt
	var mutateErr error
	if stageContext.spec.Writes && input.Apply.Changed {
		if input.Lease == nil {
			return operation.RollbackResult{}, errors.New("xRocket mutating rollback stage requires an authoritative lease")
		}
		if err := input.Lease.Validate(time.Now().UTC()); err != nil {
			return operation.RollbackResult{}, fmt.Errorf("validate xRocket rollback lease: %w", err)
		}
		if err := validateStageLease(input.Lease.Current(), stageContext.manifest.RunID, stageContext.stage.Target); err != nil {
			return operation.RollbackResult{}, err
		}
		if err := definition.validateRollbackRecoveryEvidence(ctx, expectation); err != nil {
			return operation.RollbackResult{}, err
		}
		if expectation.Etcd != nil {
			if err := definition.validateEtcdRollbackDriftGate(ctx, *expectation.Etcd); err != nil {
				return operation.RollbackResult{}, err
			}
		}
		bootIDBeforeRollback := ""
		if expectation.OS != nil {
			bootIDBeforeRollback, err = definition.rollbackInspector.CurrentBootID(ctx, *expectation.OS)
			if err != nil {
				return operation.RollbackResult{}, fmt.Errorf("inspect xRocket OS pre-rollback boot_id: %w", err)
			}
			if strings.TrimSpace(bootIDBeforeRollback) == "" {
				return operation.RollbackResult{}, errors.New("xRocket OS rollback requires a current boot_id before mutation")
			}
		}
		var receipt RollbackMutationReceipt
		receipt, mutateErr = definition.rollbackMutator.RestoreStage(ctx, expectation)
		if err := validateRollbackMutationReceipt(receipt, stageContext.spec); err != nil {
			return operation.RollbackResult{}, err
		}
		if expectation.OS != nil && receipt.State != MutationNotStarted && receipt.BootIDBeforeRollback != bootIDBeforeRollback {
			return operation.RollbackResult{}, errors.New("xRocket OS rollback receipt boot_id does not match the pre-mutation observation")
		}
		mutationPerformed = receipt.State != MutationNotStarted
		adapterReceipt = &receipt
	}
	recoveryDigest, err := recoveryContractDigest(*stageContext.manifest.Recovery)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	checkpoint := "rollback_" + stageContext.stage.Checkpoint
	receipt := rollbackStageReceipt{
		SchemaVersion:          RollbackResultSchema,
		OperationID:            OperationID,
		OperationVersion:       Metadata().Version,
		RunID:                  stageContext.manifest.RunID,
		StageID:                stageContext.stage.ID,
		StageIndex:             stageContext.stageIndex,
		NodeID:                 stageContext.manifest.NodeID,
		ParticipantNodeIDs:     append([]string(nil), stageContext.manifest.ParticipantNodeIDs...),
		Role:                   stageContext.spec.Role,
		Checkpoint:             checkpoint,
		Barrier:                stageContext.stage.Barrier,
		RestorePointID:         input.RestorePoint.ID,
		RestoreManifestSHA256:  digestBytes(input.RestorePoint.Manifest.Payload),
		RecoveryContractSHA256: recoveryDigest,
		ApplyStateSHA256:       digestBytes(input.Apply.State.Payload),
		ApplyChanged:           input.Apply.Changed,
		MutationPerformed:      mutationPerformed,
		Expectation:            expectation,
		AdapterReceipt:         adapterReceipt,
		Failed:                 mutateErr != nil,
	}
	if applyReceipt.RestoreManifestSHA256 != receipt.RestoreManifestSHA256 {
		return operation.RollbackResult{}, errors.New("xRocket rollback RestorePoint digest differs from accepted Apply evidence")
	}
	artifact, err := encodeArtifact(RollbackResultSchema, receipt)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	evidence := []operation.EvidenceRef{
		{ID: input.RestorePoint.ID, Kind: "restore_point", SHA256: receipt.RestoreManifestSHA256},
		{ID: stageContext.manifest.Recovery.Owner.OwnerID, Kind: "xrocket_recovery_owner", SHA256: receipt.RecoveryContractSHA256},
	}
	for _, recoveryArtifact := range rollbackRecoveryArtifacts(expectation) {
		evidence = append(evidence, operation.EvidenceRef{ID: recoveryArtifact.ID, Kind: recoveryArtifact.Kind, SHA256: recoveryArtifact.SHA256})
	}
	mutationState := MutationNotStarted
	if adapterReceipt != nil {
		mutationState = adapterReceipt.State
	}
	result := operation.RollbackResult{Restored: mutateErr == nil, MutationState: mutationState, Checkpoint: checkpoint, State: artifact, Evidence: evidence}
	if mutateErr == nil && expectation.OS != nil && adapterReceipt != nil && adapterReceipt.State == MutationChanged {
		result.Reconnect = &operation.ReconnectHandoff{Barrier: operation.StageBarrierAgentReconnect, Reboot: true, BootIDBefore: adapterReceipt.BootIDBeforeRollback}
	}
	return result, mutateErr
}

func (definition *Definition) validateRollbackInput(input operation.RollbackInput) (executionStageContext, applyStageReceipt, rollbackStageExpectation, error) {
	plan, stage, stageIndex, spec, err := validateStageEnvelope(input.Plan, input.Stage, input.Runtime)
	if err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	if err := validateRuntimeParameters(plan, input.Runtime.Parameters); err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	if err := operation.ValidateRestorePoint(input.RestorePoint, time.Now().UTC()); err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, fmt.Errorf("validate xRocket rollback RestorePoint: %w", err)
	}
	if input.RestorePoint.Manifest.SchemaVersion != RestorePointRollbackSchema {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, errors.New("xRocket rollback requires the v2 recovery-artifact RestorePoint schema")
	}
	manifest, err := decodeRestoreManifest(input.RestorePoint)
	if err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	if err := validateStageRestoreCorrelation(input.RestorePoint, manifest, plan, stage, stageIndex, spec); err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	applyReceipt, err := decodeApplyStageReceipt(input.Apply)
	if err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	if input.Apply.Checkpoint != applyReceipt.Checkpoint {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, errors.New("xRocket rollback ApplyResult checkpoint differs from typed Apply evidence")
	}
	if err := validateReceiptCorrelation(applyReceipt, plan, stage, stageIndex, spec, input.Runtime); err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	if applyReceipt.RestorePointID != input.RestorePoint.ID || applyReceipt.RestoreManifestSHA256 != digestBytes(input.RestorePoint.Manifest.Payload) {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, errors.New("xRocket rollback Apply/RestorePoint manifest correlation is invalid")
	}
	expectation, err := buildRollbackExpectation(manifest, spec)
	if err != nil {
		return executionStageContext{}, applyStageReceipt{}, rollbackStageExpectation{}, err
	}
	return executionStageContext{plan: plan, stage: stage, stageIndex: stageIndex, spec: spec, manifest: manifest}, applyReceipt, expectation, nil
}

func (definition *Definition) validateRollbackRecoveryEvidence(ctx context.Context, expectation rollbackStageExpectation) error {
	for _, artifact := range rollbackRecoveryArtifacts(expectation) {
		matched, err := definition.rollbackInspector.RecoveryArtifactMatches(ctx, artifact)
		if err != nil {
			return fmt.Errorf("inspect xRocket recovery artifact %s: %w", artifact.ID, err)
		}
		if !matched {
			return fmt.Errorf("xRocket recovery artifact %s hash/identity does not match the frozen RestorePoint", artifact.ID)
		}
	}
	return nil
}

func (definition *Definition) validateEtcdRollbackDriftGate(ctx context.Context, contract RollbackEtcdContract) error {
	observed, err := definition.rollbackInspector.EtcdRecoveryState(ctx, contract)
	if err != nil {
		return fmt.Errorf("inspect xRocket etcd pre-rollback state: %w", err)
	}
	if !observed.Healthy || observed.MemberID != contract.MemberID || observed.MemberCount != 1 {
		return errors.New("xRocket etcd rollback member/control identity is not provable")
	}
	if observed.LogicalKVDigest != contract.LogicalKVDigest {
		return errors.New("xRocket etcd logical KV digest drifted after the frozen recovery baseline")
	}
	return nil
}

func validateRollbackMutationReceipt(receipt RollbackMutationReceipt, spec canonicalStage) error {
	if !validSHA256Digest(receipt.Digest) {
		return errors.New("xRocket rollback adapter must return a bounded sha256 receipt digest")
	}
	switch receipt.State {
	case MutationNotStarted, MutationChanged, MutationMayHaveChanged:
	default:
		return errors.New("xRocket rollback adapter returned an invalid mutation state")
	}
	if spec.Kind == stageKindOS {
		if receipt.State != MutationNotStarted && strings.TrimSpace(receipt.BootIDBeforeRollback) == "" {
			return errors.New("xRocket OS rollback receipt must bind the pre-reboot boot_id")
		}
		return nil
	}
	if receipt.BootIDBeforeRollback != "" {
		return errors.New("non-OS xRocket rollback receipt cannot carry boot_id evidence")
	}
	return nil
}

func decodeRollbackStageReceipt(result operation.RollbackResult) (rollbackStageReceipt, error) {
	if result.State.SchemaVersion != RollbackResultSchema || result.Checkpoint == "" || len(result.State.Payload) == 0 {
		return rollbackStageReceipt{}, errors.New("xRocket RollbackResult artifact is incomplete")
	}
	decoder := json.NewDecoder(bytes.NewReader(result.State.Payload))
	decoder.DisallowUnknownFields()
	var receipt rollbackStageReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return rollbackStageReceipt{}, fmt.Errorf("decode xRocket RollbackResult: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return rollbackStageReceipt{}, errors.New("xRocket RollbackResult contains trailing JSON")
		}
		return rollbackStageReceipt{}, err
	}
	if receipt.SchemaVersion != RollbackResultSchema || receipt.OperationID != OperationID || receipt.OperationVersion != Metadata().Version || receipt.RunID == "" || receipt.StageID == "" || receipt.NodeID == "" || receipt.RestorePointID == "" || !validSHA256Digest(receipt.RestoreManifestSHA256) || !validSHA256Digest(receipt.RecoveryContractSHA256) || !validSHA256Digest(receipt.ApplyStateSHA256) {
		return rollbackStageReceipt{}, errors.New("xRocket RollbackResult identity is invalid")
	}
	if result.Restored == receipt.Failed {
		return rollbackStageReceipt{}, errors.New("xRocket RollbackResult restored flag differs from typed rollback failure state")
	}
	expectedState := MutationNotStarted
	if receipt.AdapterReceipt != nil {
		expectedState = receipt.AdapterReceipt.State
	}
	if result.MutationState != expectedState {
		return rollbackStageReceipt{}, errors.New("xRocket RollbackResult mutation state differs from typed adapter evidence")
	}
	if result.Checkpoint != receipt.Checkpoint {
		return rollbackStageReceipt{}, errors.New("xRocket RollbackResult checkpoint differs from typed rollback evidence")
	}
	if receipt.MutationPerformed && !receipt.ApplyChanged {
		return rollbackStageReceipt{}, errors.New("xRocket rollback cannot mutate when accepted Apply reported no changed state")
	}
	if receipt.ApplyChanged && receipt.AdapterReceipt == nil {
		return rollbackStageReceipt{}, errors.New("xRocket changed Apply rollback attempt is missing adapter evidence")
	}
	if receipt.AdapterReceipt != nil {
		if receipt.MutationPerformed != (receipt.AdapterReceipt.State != MutationNotStarted) {
			return rollbackStageReceipt{}, errors.New("xRocket rollback mutation flag differs from typed adapter state")
		}
	}
	if err := validateRollbackExpectation(receipt.Expectation); err != nil {
		return rollbackStageReceipt{}, err
	}
	return receipt, nil
}

func validateRollbackReceiptRestoreCorrelation(receipt rollbackStageReceipt, point operation.RestorePoint, manifest restorePointManifest) (rollbackStageExpectation, canonicalStage, error) {
	if manifest.SchemaVersion != RestorePointRollbackSchema || manifest.Recovery == nil {
		return rollbackStageExpectation{}, canonicalStage{}, errors.New("xRocket rollback verification requires a v2 RestorePoint")
	}
	if receipt.RunID != manifest.RunID || receipt.StageID != manifest.StageID || receipt.StageIndex != manifest.StageIndex || receipt.NodeID != manifest.NodeID || receipt.Role != manifest.Role || !reflect.DeepEqual(receipt.ParticipantNodeIDs, manifest.ParticipantNodeIDs) || receipt.RestorePointID != point.ID || receipt.RestoreManifestSHA256 != digestBytes(point.Manifest.Payload) {
		return rollbackStageExpectation{}, canonicalStage{}, errors.New("xRocket RollbackResult restore identity correlation is invalid")
	}
	if receipt.StageIndex < 0 || receipt.StageIndex >= len(canonicalStages) {
		return rollbackStageExpectation{}, canonicalStage{}, errors.New("xRocket RollbackResult stage index is invalid")
	}
	spec := canonicalStages[receipt.StageIndex]
	if spec.ID != receipt.StageID || spec.Role != receipt.Role || receipt.Barrier != spec.Barrier {
		return rollbackStageExpectation{}, canonicalStage{}, errors.New("xRocket RollbackResult canonical stage correlation is invalid")
	}
	recoveryDigest, err := recoveryContractDigest(*manifest.Recovery)
	if err != nil {
		return rollbackStageExpectation{}, canonicalStage{}, err
	}
	if receipt.RecoveryContractSHA256 != recoveryDigest {
		return rollbackStageExpectation{}, canonicalStage{}, errors.New("xRocket RollbackResult recovery contract digest is invalid")
	}
	expectation, err := buildRollbackExpectation(manifest, spec)
	if err != nil {
		return rollbackStageExpectation{}, canonicalStage{}, err
	}
	if !reflect.DeepEqual(receipt.Expectation, expectation) {
		return rollbackStageExpectation{}, canonicalStage{}, errors.New("xRocket RollbackResult expectation differs from the frozen recovery baseline")
	}
	if receipt.AdapterReceipt != nil {
		if err := validateRollbackMutationReceipt(*receipt.AdapterReceipt, spec); err != nil {
			return rollbackStageExpectation{}, canonicalStage{}, err
		}
	}
	return expectation, spec, nil
}

func (definition *Definition) verifyRollbackStage(ctx context.Context, input operation.VerifyRollbackInput) (operation.Verification, error) {
	plan, stage, stageIndex, spec, err := validateStageEnvelope(input.Plan, input.Stage, input.Runtime)
	if err != nil {
		return operation.Verification{}, err
	}
	if err := validateRuntimeParameters(plan, input.Runtime.Parameters); err != nil {
		return operation.Verification{}, err
	}
	manifest, err := decodeRestoreManifest(input.RestorePoint)
	if err != nil {
		return operation.Verification{}, err
	}
	if err := validateStageRestoreCorrelation(input.RestorePoint, manifest, plan, stage, stageIndex, spec); err != nil {
		return operation.Verification{}, err
	}
	receipt, err := decodeRollbackStageReceipt(input.Rollback)
	if err != nil {
		return operation.Verification{}, err
	}
	expectation, receiptSpec, err := validateRollbackReceiptRestoreCorrelation(receipt, input.RestorePoint, manifest)
	if err != nil {
		return operation.Verification{}, err
	}
	if receiptSpec.ID != spec.ID {
		return operation.Verification{}, errors.New("xRocket rollback verification stage differs from frozen plan")
	}
	if err := definition.validateRollbackRecoveryEvidence(ctx, expectation); err != nil {
		return operation.Verification{}, err
	}
	passed, err := verifyRollbackObservedBaseline(ctx, definition.rollbackInspector, expectation, receipt, input.Rollback)
	if err != nil {
		return operation.Verification{}, err
	}
	if !passed {
		return rollbackPostconditionFailure(stage.Target), nil
	}
	return operation.Verification{Passed: true, Summary: "xRocket rollback observed state matches the frozen run-owned recovery baseline"}, nil
}

func verifyRollbackObservedBaseline(ctx context.Context, inspector LocalRollbackInspectionAdapter, expectation rollbackStageExpectation, receipt rollbackStageReceipt, result operation.RollbackResult) (bool, error) {
	observation, err := inspector.InspectRollback(ctx, expectation)
	if err != nil {
		return false, fmt.Errorf("inspect xRocket rollback postcondition: %w", err)
	}
	if !observation.Satisfied {
		return false, nil
	}
	if expectation.Etcd != nil {
		if observation.Etcd == nil {
			return false, nil
		}
		value := observation.Etcd
		contract := expectation.Etcd
		if !value.Healthy || value.MemberID != contract.MemberID || value.MemberCount != 1 || value.ClientAddress != contract.OldClient || value.PeerAddress != contract.OldPeer || value.LogicalKVDigest != contract.LogicalKVDigest {
			return false, nil
		}
	}
	if expectation.OS != nil {
		if receipt.AdapterReceipt == nil || result.Reconnect == nil || result.Reconnect.Barrier != operation.StageBarrierAgentReconnect || result.Reconnect.BootIDBefore == "" || result.Reconnect.BootIDAfter == "" || result.Reconnect.BootIDBefore == result.Reconnect.BootIDAfter || observation.BootID != result.Reconnect.BootIDAfter {
			return false, nil
		}
	}
	return true, nil
}

func rollbackPostconditionFailure(target operation.Target) operation.Verification {
	return operation.Verification{
		Passed:   false,
		Summary:  "xRocket rollback postcondition does not match the frozen run-owned recovery baseline",
		Findings: []operation.Finding{{Code: rollbackPostconditionFindingCode, Severity: operation.FindingBlocking, Summary: "Observed state does not match the frozen rollback baseline", Target: &target}},
	}
}

func verifyRestoreProviderRollback(ctx context.Context, inspector LocalRollbackInspectionAdapter, point operation.RestorePoint, result operation.RollbackResult) (operation.Verification, error) {
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		return operation.Verification{}, err
	}
	receipt, err := decodeRollbackStageReceipt(result)
	if err != nil {
		return operation.Verification{}, err
	}
	expectation, _, err := validateRollbackReceiptRestoreCorrelation(receipt, point, manifest)
	if err != nil {
		return operation.Verification{}, err
	}
	for _, artifact := range rollbackRecoveryArtifacts(expectation) {
		matched, inspectErr := inspector.RecoveryArtifactMatches(ctx, artifact)
		if inspectErr != nil {
			return operation.Verification{}, inspectErr
		}
		if !matched {
			return operation.Verification{}, fmt.Errorf("xRocket restore-provider recovery artifact %s no longer matches", artifact.ID)
		}
	}
	passed, err := verifyRollbackObservedBaseline(ctx, inspector, expectation, receipt, result)
	if err != nil {
		return operation.Verification{}, err
	}
	if !passed {
		target := point.Targets[0]
		return rollbackPostconditionFailure(target), nil
	}
	return operation.Verification{Passed: true, Summary: "xRocket restore provider independently verified the frozen recovery baseline"}, nil
}
