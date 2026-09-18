package xrocketreaddress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sort"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const (
	RestorePointProviderID = "xrocket.site.readdress.restore.v1"
	RestorePointSchema     = "xrocket.readdress.restore-point.v1"
)

type restorePointProvider struct {
	probe              discoveryProbe
	now                func() time.Time
	recoveryCollector  RecoveryArtifactCollector
	rollbackInspector  LocalRollbackInspectionAdapter
}

type restorePointManifest struct {
	SchemaVersion      string             `json:"schema_version"`
	OperationID        string             `json:"operation_id"`
	OperationVersion   string             `json:"operation_version"`
	RunID              string             `json:"run_id"`
	StageID            string             `json:"stage_id"`
	StageIndex         int                `json:"stage_index"`
	NodeID             string             `json:"node_id"`
	ParticipantNodeIDs []string           `json:"participant_node_ids"`
	Role               string                    `json:"role"`
	Before             restoreBeforeState        `json:"before"`
	Recovery           *RecoveryArtifactContract `json:"recovery,omitempty"`
}

type restoreBeforeState struct {
	Site      restoreSiteState      `json:"site"`
	Network   restoreNetworkState   `json:"network"`
	Product   restoreProductState   `json:"product"`
	Database  restoreDatabaseState  `json:"external_db"`
	Etcd      restoreEtcdState      `json:"etcd"`
	HA        restoreHAState        `json:"ha"`
	Rendering restoreRenderingState `json:"rendering"`
}

type restoreSiteState struct {
	MasterAddress string `json:"master_address"`
	SlaveAddress  string `json:"slave_address"`
	VIPAddress    string `json:"vip_address"`
}

type restoreInterfaceAddress struct {
	Address      string `json:"address"`
	PrefixLength int    `json:"prefix_length"`
}

type restoreNetworkState struct {
	Address            string                    `json:"address"`
	PrefixLength       int                       `json:"prefix_length"`
	Interface          string                    `json:"interface"`
	InterfaceAddresses []restoreInterfaceAddress `json:"interface_addresses"`
	Gateway            string                    `json:"gateway"`
	Backend            string                    `json:"backend"`
	ConfigPath         string                    `json:"config_path"`
	PersistentAddress  string                    `json:"persistent_address"`
	PersistentPrefix   int                       `json:"persistent_prefix_length"`
	PersistentGateway  string                    `json:"persistent_gateway"`
}

type restoreProductState struct {
	Generation      string `json:"generation"`
	VersionEvidence string `json:"version_evidence"`
	InstallProfile  string `json:"install_profile"`
	OSAuthorityUID  int    `json:"os_authority_uid"`
	ProductUser     string `json:"product_user"`
	ProductHome     string `json:"product_home"`
	ProductPrefix   string `json:"product_prefix"`
	XrocketBinary   string `json:"xrocket_binary"`
	XrocketVersion  string `json:"xrocket_version"`
	CommonYAMLPath  string `json:"common_yaml_path"`
}

type restoreDatabaseState struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type restoreEtcdState struct {
	ConfigPath     string `json:"config_path"`
	EtcdctlPath    string `json:"etcdctl_path"`
	Scheme         string `json:"scheme"`
	ClientAddress  string `json:"client_address"`
	ClientPort     int    `json:"client_port"`
	PeerAddress    string `json:"peer_address"`
	PeerPort       int    `json:"peer_port"`
	MemberID       string `json:"member_id"`
	MemberCount    int    `json:"member_count"`
	ServiceName    string `json:"service_name"`
	ControlAdapter string `json:"control_adapter"`
}

type restoreHAState struct {
	ConfigPath     string `json:"config_path"`
	ConfiguredRole string `json:"configured_role"`
	RuntimeRole    string `json:"runtime_role"`
	Interface      string `json:"interface"`
	SourceAddress  string `json:"source_address"`
	PeerAddress    string `json:"peer_address"`
	VIPAddress     string `json:"vip_address"`
	Priority       int    `json:"priority"`
	Nopreempt      bool   `json:"nopreempt"`
	BusinessPorts  []int  `json:"business_ports"`
}

type restoreRenderingState struct {
	BootID            string   `json:"boot_id"`
	ConfdDestinations []string `json:"confd_destinations"`
}

func NewRestorePointProvider(commandExecutor executor.CommandExecutor) (operation.RestorePointProvider, error) {
	if commandExecutor == nil {
		return nil, errors.New("command executor is required")
	}
	return &restorePointProvider{
		probe: discoveryProbe{executor: commandExecutor},
		now:   func() time.Time { return time.Now().UTC() },
	}, nil
}

func newRestorePointProviderWithRecovery(commandExecutor executor.CommandExecutor, collector RecoveryArtifactCollector, inspector LocalRollbackInspectionAdapter) (*restorePointProvider, error) {
	if collector == nil || inspector == nil {
		return nil, errors.New("xRocket rollback recovery collector and inspector are required")
	}
	provider, err := NewRestorePointProvider(commandExecutor)
	if err != nil {
		return nil, err
	}
	value := provider.(*restorePointProvider)
	value.recoveryCollector = collector
	value.rollbackInspector = inspector
	return value, nil
}

func (provider *restorePointProvider) ID() string { return RestorePointProviderID }

func (provider *restorePointProvider) Create(ctx context.Context, request operation.RestorePointRequest) (operation.RestorePoint, error) {
	if request.OperationID != OperationID {
		return operation.RestorePoint{}, fmt.Errorf("restore provider only supports %s", OperationID)
	}
	if strings.TrimSpace(request.RunID) == "" {
		return operation.RestorePoint{}, errors.New("restore point run ID is required")
	}
	if request.Stage == nil {
		return operation.RestorePoint{}, errors.New("xRocket restore point requires a frozen execution stage")
	}

	plan, err := decodeExecutionPlan(request.Plan)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	stageIndex, err := resolveRestoreStage(request.Plan, *request.Stage)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	localNodeID, role, expectedLocal, expectedPeer, err := resolveRestoreIdentity(plan, *request.Stage)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	participants, err := restoreParticipants(plan)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	if err := validateRestoreTargets(request.Targets, localNodeID); err != nil {
		return operation.RestorePoint{}, err
	}

	before, err := provider.captureBeforeState(ctx, plan, role, expectedLocal, expectedPeer)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	manifest := restorePointManifest{
		SchemaVersion:      RestorePointSchema,
		OperationID:        OperationID,
		OperationVersion:   Metadata().Version,
		RunID:              request.RunID,
		StageID:            request.Stage.ID,
		StageIndex:         stageIndex,
		NodeID:             localNodeID,
		ParticipantNodeIDs: participants,
		Role:               role,
		Before:             before,
	}
	if provider.recoveryCollector != nil {
		if stageIndex < 0 || stageIndex >= len(canonicalStages) {
			return operation.RestorePoint{}, errors.New("xRocket recovery stage index is invalid")
		}
		manifest.SchemaVersion = RestorePointRollbackSchema
		owner := recoveryOwnerFromManifest(manifest)
		recovery, captureErr := provider.recoveryCollector.Capture(ctx, RecoveryArtifactCaptureRequest{
			Owner: owner, StageKind: string(canonicalStages[stageIndex].Kind), Before: before,
		})
		if captureErr != nil {
			return operation.RestorePoint{}, fmt.Errorf("capture xRocket recovery artifacts: %w", captureErr)
		}
		manifest.Recovery = &recovery
	}
	if err := validateRestoreManifest(manifest); err != nil {
		return operation.RestorePoint{}, err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return operation.RestorePoint{}, fmt.Errorf("encode xRocket restore manifest: %w", err)
	}

	createdAt := provider.now().UTC()
	point := operation.RestorePoint{
		ID:          restorePointID(request.RunID, localNodeID, request.Stage.ID),
		ProviderID:  RestorePointProviderID,
		OperationID: OperationID,
		RunID:       request.RunID,
		Status:      operation.RestorePointVerified,
		Targets:     append([]operation.Target(nil), request.Targets...),
		CreatedAt:   createdAt,
		Manifest: operation.Artifact{
			SchemaVersion: manifest.SchemaVersion,
			Payload:       payload,
		},
	}
	if manifest.Recovery != nil {
		recoveryDigest, digestErr := recoveryContractDigest(*manifest.Recovery)
		if digestErr != nil {
			return operation.RestorePoint{}, digestErr
		}
		point.Evidence = append(point.Evidence, operation.EvidenceRef{
			ID: manifest.Recovery.Owner.OwnerID, Kind: "xrocket_recovery_owner", SHA256: recoveryDigest,
		})
		for _, artifact := range manifest.Recovery.Artifacts {
			point.Evidence = append(point.Evidence, operation.EvidenceRef{ID: artifact.ID, Kind: artifact.Kind, SHA256: artifact.SHA256})
		}
	}
	if request.Retention > 0 {
		expires := createdAt.Add(request.Retention)
		point.ExpiresAt = &expires
	}
	if err := operation.ValidateRestorePoint(point, createdAt); err != nil {
		return operation.RestorePoint{}, err
	}
	if _, err := decodeRestoreManifest(point); err != nil {
		return operation.RestorePoint{}, fmt.Errorf("validate created xRocket restore manifest: %w", err)
	}
	return point, nil
}

func (*restorePointProvider) Verify(_ context.Context, point operation.RestorePoint) (operation.Verification, error) {
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		return operation.Verification{}, err
	}
	if point.OperationID != manifest.OperationID || point.RunID != manifest.RunID || point.ID != restorePointID(manifest.RunID, manifest.NodeID, manifest.StageID) {
		return operation.Verification{}, errors.New("xRocket restore point identity does not match manifest")
	}
	if err := validateRestoreTargets(point.Targets, manifest.NodeID); err != nil {
		return operation.Verification{}, err
	}
	return operation.Verification{Passed: true, Summary: "xRocket run-owned node restore baseline verified"}, nil
}

func (*restorePointProvider) Restore(context.Context, operation.RestorePoint, operation.ApplyResult) (operation.RollbackResult, error) {
	return operation.RollbackResult{}, errApplyMechanismUnverified
}

func (provider *restorePointProvider) VerifyRestored(ctx context.Context, point operation.RestorePoint, result operation.RollbackResult) (operation.Verification, error) {
	if provider.rollbackInspector == nil {
		return operation.Verification{}, errApplyMechanismUnverified
	}
	return verifyRestoreProviderRollback(ctx, provider.rollbackInspector, point, result)
}

func (provider *restorePointProvider) captureBeforeState(ctx context.Context, plan executionPlan, role, expectedLocal, expectedPeer string) (restoreBeforeState, error) {
	network, err := provider.probe.observeNetwork(ctx)
	if err != nil {
		return restoreBeforeState{}, fmt.Errorf("observe local network for restore point: %w", err)
	}
	address, prefix, gateway, device, err := network.primaryIPv4(expectedLocal)
	if err != nil {
		return restoreBeforeState{}, fmt.Errorf("resolve local restore address: %w", err)
	}
	if address != expectedLocal {
		return restoreBeforeState{}, fmt.Errorf("local restore address drifted: got %s want %s", address, expectedLocal)
	}
	interfaceAddresses, err := restoreInterfaceAddresses(network, device)
	if err != nil {
		return restoreBeforeState{}, err
	}

	keepalivedPath, keepalivedContent, found, err := provider.probe.readKeepalivedConfig(ctx)
	if err != nil {
		return restoreBeforeState{}, fmt.Errorf("read local keepalived restore state: %w", err)
	}
	if !found {
		return restoreBeforeState{}, errors.New("local keepalived restore state is missing")
	}
	ha, err := parseKeepalivedConfig(keepalivedContent)
	if err != nil {
		return restoreBeforeState{}, fmt.Errorf("parse local keepalived restore state: %w", err)
	}
	if ha.SourceAddress != expectedLocal || ha.PeerAddress != expectedPeer || ha.VIPAddress != plan.Discovery.VIPAddress {
		return restoreBeforeState{}, errors.New("local keepalived addresses do not match frozen precheck topology")
	}
	if ha.Interface != device {
		return restoreBeforeState{}, fmt.Errorf("keepalived interface %s does not match local address device %s", ha.Interface, device)
	}
	if !ha.Nopreempt || ha.Priority <= 0 || len(ha.BusinessPorts) == 0 {
		return restoreBeforeState{}, errors.New("local keepalived recovery facts are incomplete")
	}
	vipOwned := network.containsAddress(plan.Discovery.VIPAddress)
	if role == "master" && !vipOwned {
		return restoreBeforeState{}, errors.New("Master no longer owns the frozen VIP before RestorePoint creation")
	}
	if role == "slave" && vipOwned {
		return restoreBeforeState{}, errors.New("Slave unexpectedly owns the frozen VIP before RestorePoint creation")
	}
	if role == "master" {
		expectedPorts := append([]int(nil), plan.Discovery.BusinessPorts...)
		actualPorts := append([]int(nil), ha.BusinessPorts...)
		sort.Ints(expectedPorts)
		sort.Ints(actualPorts)
		if keepalivedPath != plan.Discovery.KeepalivedConfigPath || ha.Priority != plan.Discovery.KeepalivedPriority || !reflect.DeepEqual(actualPorts, expectedPorts) {
			return restoreBeforeState{}, errors.New("Master keepalived recovery facts drifted after precheck")
		}
	}

	localState := discoveryState{
		SchemaVersion:  discoverySchema,
		MasterAddress:  expectedLocal,
		PrefixLength:   prefix,
		GatewayAddress: gateway,
		Interface:      device,
	}
	profile, err := provider.probe.discoverRuntimeProfile(ctx, localState)
	if err != nil {
		return restoreBeforeState{}, fmt.Errorf("discover local runtime restore profile: %w", err)
	}
	if role == "master" {
		if !reflect.DeepEqual(profile, plan.MasterProfile) {
			return restoreBeforeState{}, errors.New("Master runtime profile drifted after precheck")
		}
	} else {
		if profile.XrocketVersion != plan.MasterProfile.XrocketVersion {
			return restoreBeforeState{}, errors.New("Slave xRocket version does not match frozen Master profile")
		}
		if profile.ExternalDBAddress != plan.MasterProfile.ExternalDBAddress || profile.ExternalDBPort != plan.MasterProfile.ExternalDBPort {
			return restoreBeforeState{}, errors.New("Slave external DB endpoint does not match frozen site endpoint")
		}
	}

	productPath, productFound, err := provider.probe.resolveProductPath(ctx)
	if err != nil {
		return restoreBeforeState{}, fmt.Errorf("resolve local xRocket product restore path: %w", err)
	}
	if !productFound {
		return restoreBeforeState{}, errors.New("local xRocket product version evidence is missing")
	}
	generation := generationPattern.FindString(productPath)
	if generation == "" || generation != plan.Discovery.ProductGeneration {
		return restoreBeforeState{}, fmt.Errorf("local product generation %q does not match frozen generation %q", generation, plan.Discovery.ProductGeneration)
	}
	if role == "master" && productPath != plan.Discovery.ProductVersionEvidence {
		return restoreBeforeState{}, errors.New("Master product version evidence drifted after precheck")
	}

	runtimeRole := "standby"
	if vipOwned {
		runtimeRole = "active_vip_owner"
	}
	confdDestinations := append([]string(nil), profile.ConfdDestinations...)
	sort.Strings(confdDestinations)
	businessPorts := append([]int(nil), ha.BusinessPorts...)
	sort.Ints(businessPorts)

	before := restoreBeforeState{
		Site: restoreSiteState{
			MasterAddress: plan.Discovery.MasterAddress,
			SlaveAddress:  plan.Discovery.SlaveAddress,
			VIPAddress:    plan.Discovery.VIPAddress,
		},
		Network: restoreNetworkState{
			Address:            address,
			PrefixLength:       prefix,
			Interface:          device,
			InterfaceAddresses: interfaceAddresses,
			Gateway:            gateway,
			Backend:            profile.NetworkBackend,
			ConfigPath:         profile.NetworkConfigPath,
			PersistentAddress:  profile.PersistentAddress,
			PersistentPrefix:   profile.PersistentPrefix,
			PersistentGateway:  profile.PersistentGateway,
		},
		Product: restoreProductState{
			Generation:      generation,
			VersionEvidence: productPath,
			InstallProfile:  profile.InstallProfile,
			OSAuthorityUID:  profile.OSAuthorityUID,
			ProductUser:     profile.ProductUser,
			ProductHome:     profile.ProductHome,
			ProductPrefix:   profile.ProductPrefix,
			XrocketBinary:   profile.XrocketBinary,
			XrocketVersion:  profile.XrocketVersion,
			CommonYAMLPath:  profile.CommonYAMLPath,
		},
		Database: restoreDatabaseState{
			Address: profile.ExternalDBAddress,
			Port:    profile.ExternalDBPort,
		},
		Etcd: restoreEtcdState{
			ConfigPath:     profile.EtcdConfigPath,
			EtcdctlPath:    profile.EtcdctlPath,
			Scheme:         profile.EtcdScheme,
			ClientAddress:  expectedLocal,
			ClientPort:     profile.EtcdClientPort,
			PeerAddress:    expectedLocal,
			PeerPort:       profile.EtcdPeerPort,
			MemberID:       profile.EtcdMemberID,
			MemberCount:    profile.EtcdMemberCount,
			ServiceName:    profile.EtcdServiceName,
			ControlAdapter: profile.ServiceControlAdapter,
		},
		HA: restoreHAState{
			ConfigPath:     keepalivedPath,
			ConfiguredRole: ha.State,
			RuntimeRole:    runtimeRole,
			Interface:      ha.Interface,
			SourceAddress:  ha.SourceAddress,
			PeerAddress:    ha.PeerAddress,
			VIPAddress:     ha.VIPAddress,
			Priority:       ha.Priority,
			Nopreempt:      ha.Nopreempt,
			BusinessPorts:  businessPorts,
		},
		Rendering: restoreRenderingState{
			BootID:            profile.BootID,
			ConfdDestinations: confdDestinations,
		},
	}
	if err := validateRestoreBeforeState(before); err != nil {
		return restoreBeforeState{}, err
	}
	return before, nil
}

func restoreInterfaceAddresses(observation networkObservation, device string) ([]restoreInterfaceAddress, error) {
	var addresses []restoreInterfaceAddress
	seen := map[string]struct{}{}
	for _, candidate := range observation.addresses {
		if candidate.IfName != device {
			continue
		}
		for _, current := range candidate.AddrInfo {
			ip := net.ParseIP(current.Local)
			if current.Family != "inet" || current.Scope != "global" || ip == nil || ip.To4() == nil || current.PrefixLen < 1 || current.PrefixLen > 32 {
				continue
			}
			address := ip.To4().String()
			key := fmt.Sprintf("%s/%d", address, current.PrefixLen)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			addresses = append(addresses, restoreInterfaceAddress{Address: address, PrefixLength: current.PrefixLen})
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("local restore interface has no bounded global IPv4 state")
	}
	sort.Slice(addresses, func(left, right int) bool {
		if addresses[left].Address != addresses[right].Address {
			return addresses[left].Address < addresses[right].Address
		}
		return addresses[left].PrefixLength < addresses[right].PrefixLength
	})
	return addresses, nil
}

func resolveRestoreStage(plan operation.Plan, stage operation.PlanStep) (int, error) {
	matches := 0
	index := -1
	for candidateIndex, candidate := range plan.Steps {
		if candidate.ID != stage.ID {
			continue
		}
		if !reflect.DeepEqual(candidate, stage) {
			return -1, fmt.Errorf("restore stage %q does not match frozen plan", stage.ID)
		}
		matches++
		index = candidateIndex
	}
	if matches != 1 {
		return -1, fmt.Errorf("restore stage %q must resolve exactly once in frozen plan", stage.ID)
	}
	return index, nil
}

func resolveRestoreIdentity(plan executionPlan, stage operation.PlanStep) (nodeID, role, localAddress, peerAddress string, err error) {
	switch stage.ExecutorNodeID {
	case plan.MasterNodeID:
		return plan.MasterNodeID, "master", plan.Discovery.MasterAddress, plan.Discovery.SlaveAddress, nil
	case plan.SlaveNodeID:
		return plan.SlaveNodeID, "slave", plan.Discovery.SlaveAddress, plan.Discovery.MasterAddress, nil
	default:
		return "", "", "", "", errors.New("restore stage executor is not a frozen xRocket participant")
	}
}

func restoreParticipants(plan executionPlan) ([]string, error) {
	master := strings.TrimSpace(plan.MasterNodeID)
	slave := strings.TrimSpace(plan.SlaveNodeID)
	if master == "" || slave == "" || master == slave {
		return nil, errors.New("xRocket restore point requires two distinct frozen participants")
	}
	participants := []string{master, slave}
	sort.Strings(participants)
	return participants, nil
}

func validateRestoreTargets(targets []operation.Target, nodeID string) error {
	if len(targets) != 1 {
		return errors.New("xRocket restore point requires exactly one node target")
	}
	if err := operation.ValidateTarget(targets[0]); err != nil {
		return err
	}
	if targets[0].Kind != operation.TargetNode || targets[0].NodeID != nodeID {
		return errors.New("xRocket restore point target does not match local frozen participant")
	}
	return nil
}

func validateRestoreManifest(manifest restorePointManifest) error {
	if (manifest.SchemaVersion != RestorePointSchema && manifest.SchemaVersion != RestorePointRollbackSchema) || manifest.OperationID != OperationID || manifest.OperationVersion != Metadata().Version {
		return errors.New("xRocket restore manifest identity is invalid")
	}
	if strings.TrimSpace(manifest.RunID) == "" || strings.TrimSpace(manifest.StageID) == "" || strings.TrimSpace(manifest.NodeID) == "" {
		return errors.New("xRocket restore manifest run, stage and node identity are required")
	}
	if manifest.StageIndex < 0 {
		return errors.New("xRocket restore manifest stage index is invalid")
	}
	if manifest.Role != "master" && manifest.Role != "slave" {
		return errors.New("xRocket restore manifest role is invalid")
	}
	if len(manifest.ParticipantNodeIDs) != 2 || manifest.ParticipantNodeIDs[0] == manifest.ParticipantNodeIDs[1] {
		return errors.New("xRocket restore manifest requires two distinct participants")
	}
	participants := append([]string(nil), manifest.ParticipantNodeIDs...)
	sort.Strings(participants)
	if !reflect.DeepEqual(participants, manifest.ParticipantNodeIDs) {
		return errors.New("xRocket restore manifest participants must be canonicalized")
	}
	if manifest.NodeID != manifest.ParticipantNodeIDs[0] && manifest.NodeID != manifest.ParticipantNodeIDs[1] {
		return errors.New("xRocket restore manifest node is not a participant")
	}
	if err := validateRestoreBeforeState(manifest.Before); err != nil {
		return err
	}
	if manifest.SchemaVersion == RestorePointRollbackSchema {
		return validateRecoveryContract(manifest)
	}
	if manifest.Recovery != nil {
		return errors.New("xRocket v1 restore manifest cannot carry rollback recovery artifacts")
	}
	return nil
}

func validateRestoreBeforeState(before restoreBeforeState) error {
	if before.Site.MasterAddress == "" || before.Site.SlaveAddress == "" || before.Site.VIPAddress == "" {
		return errors.New("xRocket restore site addresses are incomplete")
	}
	if before.Network.Address == "" || before.Network.PrefixLength <= 0 || before.Network.Interface == "" || len(before.Network.InterfaceAddresses) == 0 || before.Network.Gateway == "" || before.Network.Backend == "" || before.Network.ConfigPath == "" || before.Network.PersistentAddress == "" || before.Network.PersistentPrefix <= 0 || before.Network.PersistentGateway == "" {
		return errors.New("xRocket restore network state is incomplete")
	}
	if before.Product.Generation == "" || before.Product.VersionEvidence == "" || before.Product.InstallProfile == "" || before.Product.OSAuthorityUID != 0 || before.Product.ProductUser == "" || before.Product.ProductHome == "" || before.Product.XrocketBinary == "" || before.Product.XrocketVersion == "" || before.Product.CommonYAMLPath == "" {
		return errors.New("xRocket restore product state is incomplete")
	}
	switch before.Product.InstallProfile {
	case installProfileRootNative:
		if before.Product.ProductUser != "root" || before.Product.ProductHome != "/root" || before.Product.ProductPrefix != "" {
			return errors.New("xRocket root-native restore identity is inconsistent")
		}
	case installProfileHomePrefix:
		if before.Product.ProductUser == "root" || before.Product.ProductPrefix == "" || before.Product.ProductHome != before.Product.ProductPrefix {
			return errors.New("xRocket HOME-prefixed restore identity is inconsistent")
		}
	default:
		return errors.New("xRocket restore install profile is unsupported")
	}
	if before.Database.Address == "" || before.Database.Port <= 0 {
		return errors.New("xRocket restore external DB state is incomplete")
	}
	if before.Etcd.ConfigPath == "" || before.Etcd.EtcdctlPath == "" || before.Etcd.Scheme == "" || before.Etcd.ClientAddress == "" || before.Etcd.ClientPort <= 0 || before.Etcd.PeerAddress == "" || before.Etcd.PeerPort <= 0 || before.Etcd.MemberID == "" || before.Etcd.MemberCount != 1 || before.Etcd.ServiceName == "" || before.Etcd.ControlAdapter == "" {
		return errors.New("xRocket restore etcd state is incomplete")
	}
	if before.HA.ConfigPath == "" || before.HA.ConfiguredRole == "" || before.HA.RuntimeRole == "" || before.HA.Interface == "" || before.HA.SourceAddress == "" || before.HA.PeerAddress == "" || before.HA.VIPAddress == "" || before.HA.Priority <= 0 || !before.HA.Nopreempt || len(before.HA.BusinessPorts) == 0 {
		return errors.New("xRocket restore HA state is incomplete")
	}
	if before.Rendering.BootID == "" || len(before.Rendering.ConfdDestinations) == 0 {
		return errors.New("xRocket restore rendering state is incomplete")
	}
	return nil
}

func decodeRestoreManifest(point operation.RestorePoint) (restorePointManifest, error) {
	if point.ProviderID != RestorePointProviderID {
		return restorePointManifest{}, fmt.Errorf("unsupported xRocket restore provider %q", point.ProviderID)
	}
	if point.Manifest.SchemaVersion != RestorePointSchema && point.Manifest.SchemaVersion != RestorePointRollbackSchema {
		return restorePointManifest{}, fmt.Errorf("unsupported xRocket restore schema %q", point.Manifest.SchemaVersion)
	}
	decoder := json.NewDecoder(bytes.NewReader(point.Manifest.Payload))
	decoder.DisallowUnknownFields()
	var manifest restorePointManifest
	if err := decoder.Decode(&manifest); err != nil {
		return restorePointManifest{}, fmt.Errorf("decode xRocket restore manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return restorePointManifest{}, errors.New("xRocket restore manifest contains trailing JSON")
		}
		return restorePointManifest{}, fmt.Errorf("decode xRocket restore manifest trailing content: %w", err)
	}
	if manifest.SchemaVersion != point.Manifest.SchemaVersion {
		return restorePointManifest{}, errors.New("xRocket restore manifest schema differs from artifact schema")
	}
	if err := validateRestoreManifest(manifest); err != nil {
		return restorePointManifest{}, err
	}
	return manifest, nil
}

func restorePointID(runID, nodeID, stageID string) string {
	digest := sha256.Sum256([]byte(OperationID + "|" + Metadata().Version + "|" + runID + "|" + nodeID + "|" + stageID))
	return "xr-rp-" + hex.EncodeToString(digest[:])[:16]
}

var _ operation.RestorePointProvider = (*restorePointProvider)(nil)
