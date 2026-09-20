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
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"setpoint/internal/operation"
)

const ApplyResultSchema = "xrocket.readdress.apply-stage.v1"

type LocalMutationReceipt struct {
	Digest string `json:"digest"`
}

type AliasStageContract struct {
	NodeID       string `json:"node_id"`
	Role         string `json:"role"`
	Interface    string `json:"interface"`
	OldAddress   string `json:"old_address"`
	NewAddress   string `json:"new_address"`
	PrefixLength int    `json:"prefix_length"`
	Gateway      string `json:"gateway"`
}

type ProductStageContract struct {
	NodeID           string              `json:"node_id"`
	Role             string              `json:"role"`
	InstallProfile   string              `json:"install_profile"`
	ProductUser      string              `json:"product_user"`
	ProductHome      string              `json:"product_home"`
	ProductPrefix    string              `json:"product_prefix"`
	XrocketBinary    string              `json:"xrocket_binary"`
	OldMaster        string              `json:"old_master"`
	OldSlave         string              `json:"old_slave"`
	OldVIP           string              `json:"old_vip"`
	NewMaster        string              `json:"new_master"`
	NewSlave         string              `json:"new_slave"`
	NewVIP           string              `json:"new_vip"`
	ProductBundle    RecoveryArtifactRef `json:"product_bundle,omitempty"`
	KeepalivedConfig RecoveryArtifactRef `json:"keepalived_config,omitempty"`
}

type ExternalDBStageContract struct {
	NodeID                   string              `json:"node_id"`
	Role                     string              `json:"role"`
	CommonYAMLPath           string              `json:"common_yaml_path"`
	OldAddress               string              `json:"old_address"`
	NewAddress               string              `json:"new_address"`
	Port                     int                 `json:"port"`
	PreserveNonAddressFields bool                `json:"preserve_non_address_fields"`
	BaselineConfig           RecoveryArtifactRef `json:"baseline_config,omitempty"`
}

type EtcdStageContract struct {
	NodeID         string `json:"node_id"`
	Role           string `json:"role"`
	ConfigPath     string `json:"config_path"`
	EtcdctlPath    string `json:"etcdctl_path"`
	Scheme         string `json:"scheme"`
	MemberID       string `json:"member_id"`
	MemberCount    int    `json:"member_count"`
	OldClient      string `json:"old_client_address"`
	NewClient      string `json:"new_client_address"`
	ClientPort     int    `json:"client_port"`
	OldPeer        string `json:"old_peer_address"`
	NewPeer        string `json:"new_peer_address"`
	PeerPort       int    `json:"peer_port"`
	ServiceName    string `json:"service_name"`
	ControlAdapter string `json:"control_adapter"`
}

type ConfdStageContract struct {
	NodeID             string                `json:"node_id"`
	Role               string                `json:"role"`
	InstallProfile     string                `json:"install_profile"`
	ProductUser        string                `json:"product_user"`
	ProductHome        string                `json:"product_home"`
	ProductPrefix      string                `json:"product_prefix"`
	XrocketBinary      string                `json:"xrocket_binary"`
	Destinations       []string              `json:"destinations"`
	DestinationFiles   []RecoveryArtifactRef `json:"destination_files,omitempty"`
	OldMaster          string                `json:"old_master"`
	OldSlave           string                `json:"old_slave"`
	OldVIP             string                `json:"old_vip"`
	OldExternalDB      string                `json:"old_external_db"`
	ExpectedMaster     string                `json:"expected_master"`
	ExpectedSlave      string                `json:"expected_slave"`
	ExpectedVIP        string                `json:"expected_vip"`
	ExpectedExternalDB string                `json:"expected_external_db"`
}

type OSStageContract struct {
	NodeID             string                    `json:"node_id"`
	Role               string                    `json:"role"`
	Interface          string                    `json:"interface"`
	ConfigPath         string                    `json:"config_path"`
	OldAddress         string                    `json:"old_address"`
	NewAddress         string                    `json:"new_address"`
	PrefixLength       int                       `json:"prefix_length"`
	Gateway            string                    `json:"gateway"`
	InterfaceAddresses []restoreInterfaceAddress `json:"interface_addresses"`
	BootIDBefore       string                    `json:"boot_id_before"`
	Barrier            operation.StageBarrier    `json:"barrier"`
	Reboot             bool                      `json:"reboot"`
}

type FinalSiteStageContract struct {
	NodeID        string `json:"node_id"`
	Role          string `json:"role"`
	MasterAddress string `json:"master_address"`
	SlaveAddress  string `json:"slave_address"`
	VIPAddress    string `json:"vip_address"`
	Nopreempt     bool   `json:"nopreempt"`
	BusinessPorts []int  `json:"business_ports"`
}

type LocalMutationAdapter interface {
	AddTargetAlias(context.Context, AliasStageContract) (LocalMutationReceipt, error)
	ReaddressProduct(context.Context, ProductStageContract) (LocalMutationReceipt, error)
	UpdateExternalDB(context.Context, ExternalDBStageContract) (LocalMutationReceipt, error)
	ReaddressEtcd(context.Context, EtcdStageContract) (LocalMutationReceipt, error)
	RenderConfd(context.Context, ConfdStageContract) (LocalMutationReceipt, error)
	CutoverOS(context.Context, OSStageContract) (LocalMutationReceipt, error)
}

type LocalInspectionAdapter interface {
	AliasSatisfied(context.Context, AliasStageContract) (bool, error)
	ProductSatisfied(context.Context, ProductStageContract) (bool, error)
	ExternalDBSatisfied(context.Context, ExternalDBStageContract) (bool, error)
	EtcdSatisfied(context.Context, EtcdStageContract) (bool, error)
	ConfdSatisfied(context.Context, ConfdStageContract) (bool, error)
	OSSatisfied(context.Context, OSStageContract) (bool, error)
	FinalSiteSatisfied(context.Context, FinalSiteStageContract) (bool, error)
}

type stageKind string

const (
	stageKindSnapshot   stageKind = "snapshot"
	stageKindAlias      stageKind = "alias"
	stageKindProduct    stageKind = "product"
	stageKindExternalDB stageKind = "external_db"
	stageKindEtcd       stageKind = "etcd"
	stageKindConfd      stageKind = "confd"
	stageKindOS         stageKind = "os"
	stageKindFinal      stageKind = "final"
)

type canonicalStage struct {
	ID             string
	Role           string
	Kind           stageKind
	Action         string
	Checkpoint     string
	Writes         bool
	RetrySafe      bool
	RollbackAction string
	Barrier        operation.StageBarrier
}

var canonicalStages = []canonicalStage{
	{ID: "snapshot-slave", Role: "slave", Kind: stageKindSnapshot, Action: "snapshot_baseline", Checkpoint: "slave_restorepoint_verified", Writes: false, RetrySafe: true},
	{ID: "snapshot-master", Role: "master", Kind: stageKindSnapshot, Action: "snapshot_baseline", Checkpoint: "master_restorepoint_verified", Writes: false, RetrySafe: true},
	{ID: "alias-slave", Role: "slave", Kind: stageKindAlias, Action: "add_target_alias", Checkpoint: "slave_alias_verified", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "alias-master", Role: "master", Kind: stageKindAlias, Action: "add_target_alias", Checkpoint: "master_alias_verified", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "product-slave", Role: "slave", Kind: stageKindProduct, Action: "product_update_ip", Checkpoint: "slave_product_readdressed", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "product-master", Role: "master", Kind: stageKindProduct, Action: "product_update_ip", Checkpoint: "master_product_readdressed", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "external-db-slave", Role: "slave", Kind: stageKindExternalDB, Action: "external_db_ip", Checkpoint: "slave_external_db_updated", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "external-db-master", Role: "master", Kind: stageKindExternalDB, Action: "external_db_ip", Checkpoint: "master_external_db_updated", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "etcd-slave", Role: "slave", Kind: stageKindEtcd, Action: "etcd_readdress", Checkpoint: "slave_etcd_healthy", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "etcd-master", Role: "master", Kind: stageKindEtcd, Action: "etcd_readdress", Checkpoint: "master_etcd_healthy", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "confd-slave", Role: "slave", Kind: stageKindConfd, Action: "confd_render", Checkpoint: "slave_confd_verified", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "confd-master", Role: "master", Kind: stageKindConfd, Action: "confd_render", Checkpoint: "master_confd_verified", Writes: true, RollbackAction: "restore_baseline"},
	{ID: "os-slave", Role: "slave", Kind: stageKindOS, Action: "os_cutover_reboot", Checkpoint: "slave_reconnected_verified", Writes: true, RollbackAction: "restore_baseline", Barrier: operation.StageBarrierAgentReconnect},
	{ID: "os-master", Role: "master", Kind: stageKindOS, Action: "os_cutover_reboot", Checkpoint: "master_reconnected_verified", Writes: true, RollbackAction: "restore_baseline", Barrier: operation.StageBarrierAgentReconnect},
	{ID: "final-slave", Role: "slave", Kind: stageKindFinal, Action: "final_site_verify", Checkpoint: "site_readdress_verified", Writes: false, RetrySafe: true},
}

type stageExpectation struct {
	Kind       stageKind                `json:"kind"`
	Alias      *AliasStageContract      `json:"alias,omitempty"`
	Product    *ProductStageContract    `json:"product,omitempty"`
	ExternalDB *ExternalDBStageContract `json:"external_db,omitempty"`
	Etcd       *EtcdStageContract       `json:"etcd,omitempty"`
	Confd      *ConfdStageContract      `json:"confd,omitempty"`
	OS         *OSStageContract         `json:"os,omitempty"`
	Final      *FinalSiteStageContract  `json:"final,omitempty"`
}

type applyStageReceipt struct {
	SchemaVersion         string                 `json:"schema_version"`
	OperationID           string                 `json:"operation_id"`
	OperationVersion      string                 `json:"operation_version"`
	RunID                 string                 `json:"run_id"`
	StageID               string                 `json:"stage_id"`
	StageIndex            int                    `json:"stage_index"`
	NodeID                string                 `json:"node_id"`
	ParticipantNodeIDs    []string               `json:"participant_node_ids"`
	Role                  string                 `json:"role"`
	Action                string                 `json:"action"`
	Checkpoint            string                 `json:"checkpoint"`
	Writes                bool                   `json:"writes"`
	Barrier               operation.StageBarrier `json:"barrier,omitempty"`
	RestorePointID        string                 `json:"restore_point_id"`
	RestoreManifestSHA256 string                 `json:"restore_manifest_sha256"`
	Expectation           stageExpectation       `json:"expectation"`
	AlreadySatisfied      bool                   `json:"already_satisfied"`
	AdapterReceipt        *LocalMutationReceipt  `json:"adapter_receipt,omitempty"`
}

type executionStageContext struct {
	plan        executionPlan
	stage       operation.PlanStep
	stageIndex  int
	spec        canonicalStage
	manifest    restorePointManifest
	expectation stageExpectation
}

func (definition *Definition) applyStage(ctx context.Context, input operation.ApplyInput) (operation.ApplyResult, error) {
	if definition.mutator == nil || definition.inspector == nil {
		return operation.ApplyResult{}, errApplyMechanismUnverified
	}
	stageContext, err := definition.validateApplyInput(input)
	if err != nil {
		return operation.ApplyResult{}, err
	}

	alreadySatisfied := false
	var adapterReceipt *LocalMutationReceipt
	if stageContext.spec.Writes {
		if input.Lease == nil {
			return operation.ApplyResult{}, errors.New("xRocket mutating stage requires an authoritative lease")
		}
		if err := input.Lease.Validate(time.Now().UTC()); err != nil {
			return operation.ApplyResult{}, fmt.Errorf("validate xRocket stage lease: %w", err)
		}
		if err := validateStageLease(input.Lease.Current(), stageContext.manifest.RunID, stageContext.stage.Target); err != nil {
			return operation.ApplyResult{}, err
		}
		alreadySatisfied, err = definition.inspectExpectation(ctx, stageContext.expectation)
		if err != nil {
			return operation.ApplyResult{}, fmt.Errorf("inspect xRocket stage before mutation: %w", err)
		}
		if !alreadySatisfied {
			receipt, mutateErr := definition.mutateExpectation(ctx, stageContext.expectation)
			if mutateErr != nil {
				return operation.ApplyResult{}, mutateErr
			}
			if err := validateLocalMutationReceipt(receipt); err != nil {
				return operation.ApplyResult{}, err
			}
			adapterReceipt = &receipt
		}
	}

	receipt := applyStageReceipt{
		SchemaVersion:         ApplyResultSchema,
		OperationID:           OperationID,
		OperationVersion:      Metadata().Version,
		RunID:                 stageContext.manifest.RunID,
		StageID:               stageContext.stage.ID,
		StageIndex:            stageContext.stageIndex,
		NodeID:                stageContext.manifest.NodeID,
		ParticipantNodeIDs:    append([]string(nil), stageContext.manifest.ParticipantNodeIDs...),
		Role:                  stageContext.spec.Role,
		Action:                stageContext.stage.Action,
		Checkpoint:            stageContext.stage.Checkpoint,
		Writes:                stageContext.stage.Writes,
		Barrier:               stageContext.stage.Barrier,
		RestorePointID:        input.RestorePoint.ID,
		RestoreManifestSHA256: digestBytes(input.RestorePoint.Manifest.Payload),
		Expectation:           stageContext.expectation,
		AlreadySatisfied:      alreadySatisfied,
		AdapterReceipt:        adapterReceipt,
	}
	artifact, err := encodeArtifact(ApplyResultSchema, receipt)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	return operation.ApplyResult{
		Changed:    stageContext.spec.Writes && !alreadySatisfied,
		Checkpoint: stageContext.stage.Checkpoint,
		State:      artifact,
		Evidence: []operation.EvidenceRef{
			{ID: input.RestorePoint.ID, Kind: "restore_point", SHA256: receipt.RestoreManifestSHA256},
			{ID: stageContext.stage.ID, Kind: "xrocket_stage"},
			{ID: stageContext.manifest.NodeID, Kind: "xrocket_stage_executor"},
		},
	}, nil
}

func (definition *Definition) verifyStage(ctx context.Context, input operation.VerifyInput) (operation.Verification, error) {
	if definition.inspector == nil {
		return operation.Verification{}, errApplyMechanismUnverified
	}
	plan, stage, stageIndex, spec, err := validateStageEnvelope(input.Plan, input.Stage, input.Runtime)
	if err != nil {
		return operation.Verification{}, err
	}
	if err := validateRuntimeParameters(plan, input.Runtime.Parameters); err != nil {
		return operation.Verification{}, err
	}
	receipt, err := decodeApplyStageReceipt(input.Apply)
	if err != nil {
		return operation.Verification{}, err
	}
	if err := validateReceiptCorrelation(receipt, plan, stage, stageIndex, spec, input.Runtime); err != nil {
		return operation.Verification{}, err
	}
	if spec.Kind == stageKindSnapshot {
		return operation.Verification{Passed: true, Summary: "xRocket snapshot stage identity remained correlated to the verified RestorePoint receipt"}, nil
	}
	passed, err := definition.inspectExpectation(ctx, receipt.Expectation)
	if err != nil {
		return operation.Verification{}, fmt.Errorf("inspect xRocket stage postcondition: %w", err)
	}
	if !passed {
		return operation.Verification{
			Passed:   false,
			Summary:  "xRocket stage postcondition does not match the frozen bounded contract",
			Findings: []operation.Finding{{Code: "XROCKET_STAGE_POSTCONDITION_MISMATCH", Severity: operation.FindingBlocking, Summary: "Observed state does not match the expected stage postcondition", Target: &stage.Target}},
		}, nil
	}
	return operation.Verification{Passed: true, Summary: "xRocket stage postcondition matches the frozen bounded contract"}, nil
}

func (definition *Definition) validateApplyInput(input operation.ApplyInput) (executionStageContext, error) {
	plan, stage, stageIndex, spec, err := validateStageEnvelope(input.Plan, input.Stage, input.Runtime)
	if err != nil {
		return executionStageContext{}, err
	}
	if err := validateRuntimeParameters(plan, input.Runtime.Parameters); err != nil {
		return executionStageContext{}, err
	}
	if err := operation.ValidateRestorePoint(input.RestorePoint, time.Now().UTC()); err != nil {
		return executionStageContext{}, fmt.Errorf("validate xRocket stage RestorePoint: %w", err)
	}
	if input.RestorePoint.Status != operation.RestorePointVerified {
		return executionStageContext{}, errors.New("xRocket Apply requires a verified pre-mutation RestorePoint")
	}
	manifest, err := decodeRestoreManifest(input.RestorePoint)
	if err != nil {
		return executionStageContext{}, err
	}
	if err := validateStageRestoreCorrelation(input.RestorePoint, manifest, plan, stage, stageIndex, spec); err != nil {
		return executionStageContext{}, err
	}
	expectation, err := buildStageExpectation(plan, manifest, spec)
	if err != nil {
		return executionStageContext{}, err
	}
	return executionStageContext{plan: plan, stage: stage, stageIndex: stageIndex, spec: spec, manifest: manifest, expectation: expectation}, nil
}

func validateStageEnvelope(planValue operation.Plan, stagePtr *operation.PlanStep, runtime operation.RuntimeInput) (executionPlan, operation.PlanStep, int, canonicalStage, error) {
	if stagePtr == nil {
		return executionPlan{}, operation.PlanStep{}, -1, canonicalStage{}, errors.New("xRocket Apply/Verify requires a frozen stage")
	}
	plan, err := decodeExecutionPlan(planValue)
	if err != nil {
		return executionPlan{}, operation.PlanStep{}, -1, canonicalStage{}, err
	}
	if err := validateCanonicalPlan(planValue, plan); err != nil {
		return executionPlan{}, operation.PlanStep{}, -1, canonicalStage{}, err
	}
	stage := *stagePtr
	stageIndex, err := resolveRestoreStage(planValue, stage)
	if err != nil {
		return executionPlan{}, operation.PlanStep{}, -1, canonicalStage{}, err
	}
	spec := canonicalStages[stageIndex]
	if stage.ID != spec.ID {
		return executionPlan{}, operation.PlanStep{}, -1, canonicalStage{}, errors.New("xRocket stage index/identity mismatch")
	}
	if len(runtime.Targets) != 1 || !reflect.DeepEqual(runtime.Targets[0], stage.Target) {
		return executionPlan{}, operation.PlanStep{}, -1, canonicalStage{}, errors.New("xRocket local runtime target does not match the frozen stage target")
	}
	return plan, stage, stageIndex, spec, nil
}

func validateCanonicalPlan(planValue operation.Plan, plan executionPlan) error {
	if len(planValue.Steps) != len(canonicalStages) {
		return fmt.Errorf("xRocket execution plan requires exactly %d stages", len(canonicalStages))
	}
	if plan.MasterNodeID == plan.SlaveNodeID || plan.MasterNodeID == "" || plan.SlaveNodeID == "" {
		return errors.New("xRocket execution plan participant identity is invalid")
	}
	for index, spec := range canonicalStages {
		stage := planValue.Steps[index]
		expectedNode := plan.MasterNodeID
		if spec.Role == "slave" {
			expectedNode = plan.SlaveNodeID
		}
		if stage.ID != spec.ID || stage.Action != spec.Action || stage.Checkpoint != spec.Checkpoint || stage.Writes != spec.Writes || stage.RetrySafe != spec.RetrySafe || stage.RollbackAction != spec.RollbackAction || stage.Barrier != spec.Barrier {
			return fmt.Errorf("xRocket stage %d contract drifted from canonical %s", index, spec.ID)
		}
		if stage.Target.Kind != operation.TargetNode || stage.Target.NodeID != expectedNode || stage.ExecutorNodeID != expectedNode || stage.Target.NodeID != stage.ExecutorNodeID {
			return fmt.Errorf("xRocket stage %s is not bound to its intended local participant", spec.ID)
		}
	}
	return nil
}

func validateRuntimeParameters(plan executionPlan, raw json.RawMessage) error {
	value, err := decodeParameters(raw)
	if err != nil {
		return fmt.Errorf("validate xRocket runtime parameters: %w", err)
	}
	if !reflect.DeepEqual(value, plan.Parameters) {
		return errors.New("xRocket runtime parameters differ from the frozen execution plan")
	}
	return nil
}

func validateStageRestoreCorrelation(point operation.RestorePoint, manifest restorePointManifest, plan executionPlan, stage operation.PlanStep, stageIndex int, spec canonicalStage) error {
	participants, err := restoreParticipants(plan)
	if err != nil {
		return err
	}
	expectedNode := plan.MasterNodeID
	expectedLocal := plan.Discovery.MasterAddress
	expectedPeer := plan.Discovery.SlaveAddress
	if spec.Role == "slave" {
		expectedNode = plan.SlaveNodeID
		expectedLocal = plan.Discovery.SlaveAddress
		expectedPeer = plan.Discovery.MasterAddress
	}
	if manifest.OperationID != OperationID || manifest.OperationVersion != Metadata().Version || point.OperationID != OperationID || point.RunID != manifest.RunID {
		return errors.New("xRocket RestorePoint operation/version/run correlation is invalid")
	}
	if manifest.StageID != stage.ID || manifest.StageIndex != stageIndex || manifest.NodeID != expectedNode || manifest.Role != spec.Role {
		return errors.New("xRocket RestorePoint stage/node/role correlation is invalid")
	}
	if !reflect.DeepEqual(manifest.ParticipantNodeIDs, participants) {
		return errors.New("xRocket RestorePoint participant correlation is invalid")
	}
	if point.ID != restorePointID(manifest.RunID, expectedNode, stage.ID) || len(point.Targets) != 1 || !reflect.DeepEqual(point.Targets[0], stage.Target) {
		return errors.New("xRocket RestorePoint target identity is invalid")
	}
	before := manifest.Before
	if before.Site.MasterAddress != plan.Discovery.MasterAddress || before.Site.SlaveAddress != plan.Discovery.SlaveAddress || before.Site.VIPAddress != plan.Discovery.VIPAddress {
		return errors.New("xRocket RestorePoint old site state differs from the frozen plan")
	}
	if before.Network.Address != expectedLocal || before.Network.PrefixLength != plan.Discovery.PrefixLength || before.Network.Interface != plan.Discovery.Interface || before.Network.Gateway != plan.Discovery.GatewayAddress {
		return errors.New("xRocket RestorePoint old network state differs from the frozen participant")
	}
	if before.HA.SourceAddress != expectedLocal || before.HA.PeerAddress != expectedPeer || before.HA.VIPAddress != plan.Discovery.VIPAddress || !before.HA.Nopreempt {
		return errors.New("xRocket RestorePoint old HA state differs from the frozen topology")
	}
	if before.Etcd.ClientAddress != expectedLocal || before.Etcd.PeerAddress != expectedLocal || before.Etcd.MemberCount != 1 || before.Etcd.MemberID == "" {
		return errors.New("xRocket RestorePoint old singleton etcd state is invalid")
	}
	if before.Product.Generation != plan.Discovery.ProductGeneration || before.Product.XrocketVersion != plan.MasterProfile.XrocketVersion {
		return errors.New("xRocket RestorePoint product identity differs from the frozen plan")
	}
	if before.Database.Address != plan.MasterProfile.ExternalDBAddress || before.Database.Port != plan.MasterProfile.ExternalDBPort {
		return errors.New("xRocket RestorePoint external DB endpoint differs from the frozen plan")
	}
	if !boundedAbsolutePath(before.Product.XrocketBinary) || !boundedAbsolutePath(before.Product.CommonYAMLPath) || !boundedAbsolutePath(before.Network.ConfigPath) || !boundedAbsolutePath(before.Etcd.ConfigPath) || !boundedAbsolutePath(before.Etcd.EtcdctlPath) {
		return errors.New("xRocket RestorePoint contains an unbounded execution path")
	}
	for _, destination := range before.Rendering.ConfdDestinations {
		if !boundedAbsolutePath(destination) {
			return errors.New("xRocket RestorePoint contains an unbounded confd destination")
		}
	}
	return nil
}

func buildStageExpectation(plan executionPlan, manifest restorePointManifest, spec canonicalStage) (stageExpectation, error) {
	before := manifest.Before
	localNew := plan.Parameters.MasterTargetAddress
	if spec.Role == "slave" {
		localNew = plan.Parameters.SlaveTargetAddress
	}
	expectation := stageExpectation{Kind: spec.Kind}
	switch spec.Kind {
	case stageKindSnapshot:
		return expectation, nil
	case stageKindAlias:
		expectation.Alias = &AliasStageContract{NodeID: manifest.NodeID, Role: spec.Role, Interface: before.Network.Interface, OldAddress: before.Network.Address, NewAddress: localNew, PrefixLength: before.Network.PrefixLength, Gateway: before.Network.Gateway}
	case stageKindProduct:
		expectation.Product = &ProductStageContract{NodeID: manifest.NodeID, Role: spec.Role, InstallProfile: before.Product.InstallProfile, ProductUser: before.Product.ProductUser, ProductHome: before.Product.ProductHome, ProductPrefix: before.Product.ProductPrefix, XrocketBinary: before.Product.XrocketBinary, OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, NewMaster: plan.Parameters.MasterTargetAddress, NewSlave: plan.Parameters.SlaveTargetAddress, NewVIP: plan.Parameters.VIPTargetAddress}
	case stageKindExternalDB:
		expectation.ExternalDB = &ExternalDBStageContract{NodeID: manifest.NodeID, Role: spec.Role, CommonYAMLPath: before.Product.CommonYAMLPath, OldAddress: before.Database.Address, NewAddress: plan.Parameters.ExternalDBTargetAddress, Port: before.Database.Port, PreserveNonAddressFields: true}
	case stageKindEtcd:
		expectation.Etcd = &EtcdStageContract{NodeID: manifest.NodeID, Role: spec.Role, ConfigPath: before.Etcd.ConfigPath, EtcdctlPath: before.Etcd.EtcdctlPath, Scheme: before.Etcd.Scheme, MemberID: before.Etcd.MemberID, MemberCount: before.Etcd.MemberCount, OldClient: before.Etcd.ClientAddress, NewClient: localNew, ClientPort: before.Etcd.ClientPort, OldPeer: before.Etcd.PeerAddress, NewPeer: localNew, PeerPort: before.Etcd.PeerPort, ServiceName: before.Etcd.ServiceName, ControlAdapter: before.Etcd.ControlAdapter}
	case stageKindConfd:
		destinations := append([]string(nil), before.Rendering.ConfdDestinations...)
		sort.Strings(destinations)
		expectation.Confd = &ConfdStageContract{NodeID: manifest.NodeID, Role: spec.Role, InstallProfile: before.Product.InstallProfile, ProductUser: before.Product.ProductUser, ProductHome: before.Product.ProductHome, ProductPrefix: before.Product.ProductPrefix, XrocketBinary: before.Product.XrocketBinary, Destinations: destinations, OldMaster: before.Site.MasterAddress, OldSlave: before.Site.SlaveAddress, OldVIP: before.Site.VIPAddress, OldExternalDB: before.Database.Address, ExpectedMaster: plan.Parameters.MasterTargetAddress, ExpectedSlave: plan.Parameters.SlaveTargetAddress, ExpectedVIP: plan.Parameters.VIPTargetAddress, ExpectedExternalDB: plan.Parameters.ExternalDBTargetAddress}
	case stageKindOS:
		addresses := append([]restoreInterfaceAddress(nil), before.Network.InterfaceAddresses...)
		expectation.OS = &OSStageContract{NodeID: manifest.NodeID, Role: spec.Role, Interface: before.Network.Interface, ConfigPath: before.Network.ConfigPath, OldAddress: before.Network.PersistentAddress, NewAddress: localNew, PrefixLength: before.Network.PersistentPrefix, Gateway: before.Network.PersistentGateway, InterfaceAddresses: addresses, BootIDBefore: before.Rendering.BootID, Barrier: spec.Barrier, Reboot: true}
	case stageKindFinal:
		ports := append([]int(nil), before.HA.BusinessPorts...)
		sort.Ints(ports)
		expectation.Final = &FinalSiteStageContract{NodeID: manifest.NodeID, Role: spec.Role, MasterAddress: plan.Parameters.MasterTargetAddress, SlaveAddress: plan.Parameters.SlaveTargetAddress, VIPAddress: plan.Parameters.VIPTargetAddress, Nopreempt: true, BusinessPorts: ports}
	default:
		return stageExpectation{}, fmt.Errorf("unsupported xRocket stage kind %q", spec.Kind)
	}
	if manifest.SchemaVersion == RestorePointRollbackSchema {
		if err := bindStageRecoveryBaselines(&expectation, manifest); err != nil {
			return stageExpectation{}, err
		}
	}
	if err := validateStageExpectation(expectation); err != nil {
		return stageExpectation{}, err
	}
	return expectation, nil
}

func bindStageRecoveryBaselines(expectation *stageExpectation, manifest restorePointManifest) error {
	if manifest.Recovery == nil {
		return errors.New("xRocket v2 stage expectation requires recovery artifacts")
	}
	recovery := *manifest.Recovery
	before := manifest.Before
	artifact := func(kind, source string) (RecoveryArtifactRef, error) {
		return recoveryArtifactByKindSource(recovery, kind, source)
	}
	switch expectation.Kind {
	case stageKindProduct:
		bundle, err := artifact(recoveryKindProductBundle, before.Product.VersionEvidence)
		if err != nil {
			return err
		}
		keepalived, err := artifact(recoveryKindKeepalivedConfig, before.HA.ConfigPath)
		if err != nil {
			return err
		}
		expectation.Product.ProductBundle = bundle
		expectation.Product.KeepalivedConfig = keepalived
	case stageKindExternalDB:
		baseline, err := artifact(recoveryKindCommonYAML, before.Product.CommonYAMLPath)
		if err != nil {
			return err
		}
		expectation.ExternalDB.BaselineConfig = baseline
	case stageKindConfd:
		files := make([]RecoveryArtifactRef, 0, len(expectation.Confd.Destinations))
		for _, destination := range expectation.Confd.Destinations {
			baseline, err := artifact(recoveryKindConfdDestination, destination)
			if err != nil {
				return err
			}
			files = append(files, baseline)
		}
		expectation.Confd.DestinationFiles = files
	}
	return nil
}

func hasRecoveryArtifactRef(ref RecoveryArtifactRef) bool {
	return ref.ID != "" || ref.OwnerID != "" || ref.Kind != "" || ref.SourcePath != "" || ref.BackupRef != "" || ref.SHA256 != ""
}

func validateOptionalStageRecoveryRef(ref RecoveryArtifactRef, kind, source string) error {
	if !hasRecoveryArtifactRef(ref) {
		return nil
	}
	if ref.Kind != kind || ref.SourcePath != source {
		return errors.New("xRocket stage recovery baseline kind/source correlation is invalid")
	}
	return validateRecoveryArtifact(ref, RecoveryArtifactOwner{OwnerID: ref.OwnerID})
}

func stageExpectationRecoveryRefs(expectation stageExpectation) []RecoveryArtifactRef {
	switch expectation.Kind {
	case stageKindProduct:
		if expectation.Product != nil {
			return []RecoveryArtifactRef{expectation.Product.ProductBundle, expectation.Product.KeepalivedConfig}
		}
	case stageKindExternalDB:
		if expectation.ExternalDB != nil {
			return []RecoveryArtifactRef{expectation.ExternalDB.BaselineConfig}
		}
	case stageKindConfd:
		if expectation.Confd != nil {
			return append([]RecoveryArtifactRef(nil), expectation.Confd.DestinationFiles...)
		}
	}
	return nil
}

func validateApplyReceiptRecoveryOwner(receipt applyStageReceipt) error {
	ownerID := recoveryOwnerID(restorePointManifest{
		RunID:              receipt.RunID,
		StageID:            receipt.StageID,
		StageIndex:         receipt.StageIndex,
		NodeID:             receipt.NodeID,
		ParticipantNodeIDs: append([]string(nil), receipt.ParticipantNodeIDs...),
		Role:               receipt.Role,
	})
	for _, ref := range stageExpectationRecoveryRefs(receipt.Expectation) {
		if !hasRecoveryArtifactRef(ref) {
			continue
		}
		if ref.OwnerID != ownerID {
			return errors.New("xRocket ApplyResult recovery baseline owner differs from the run/stage identity")
		}
	}
	return nil
}

func validateStageExpectation(expectation stageExpectation) error {
	set := 0
	for _, value := range []any{expectation.Alias, expectation.Product, expectation.ExternalDB, expectation.Etcd, expectation.Confd, expectation.OS, expectation.Final} {
		if !isNilInterface(value) {
			set++
		}
	}
	if expectation.Kind == stageKindSnapshot {
		if set != 0 {
			return errors.New("snapshot stage cannot carry a mutation expectation")
		}
		return nil
	}
	if set != 1 {
		return errors.New("xRocket stage expectation must contain exactly one typed contract")
	}
	switch expectation.Kind {
	case stageKindAlias:
		if expectation.Alias == nil || expectation.Alias.NodeID == "" || expectation.Alias.OldAddress == "" || expectation.Alias.NewAddress == "" || expectation.Alias.Interface == "" || expectation.Alias.PrefixLength <= 0 || expectation.Alias.Gateway == "" {
			return errors.New("xRocket alias stage contract is incomplete")
		}
	case stageKindProduct:
		if expectation.Product == nil || expectation.Product.NodeID == "" || !boundedAbsolutePath(expectation.Product.XrocketBinary) || expectation.Product.OldMaster == "" || expectation.Product.OldSlave == "" || expectation.Product.OldVIP == "" || expectation.Product.NewMaster == "" || expectation.Product.NewSlave == "" || expectation.Product.NewVIP == "" {
			return errors.New("xRocket product stage contract is incomplete")
		}
		productBundleBound := hasRecoveryArtifactRef(expectation.Product.ProductBundle)
		keepalivedBound := hasRecoveryArtifactRef(expectation.Product.KeepalivedConfig)
		if productBundleBound != keepalivedBound {
			return errors.New("xRocket product stage recovery baselines must be bound together")
		}
		if productBundleBound {
			if err := validateOptionalStageRecoveryRef(expectation.Product.ProductBundle, recoveryKindProductBundle, expectation.Product.ProductBundle.SourcePath); err != nil {
				return err
			}
			if err := validateOptionalStageRecoveryRef(expectation.Product.KeepalivedConfig, recoveryKindKeepalivedConfig, expectation.Product.KeepalivedConfig.SourcePath); err != nil {
				return err
			}
		}
	case stageKindExternalDB:
		if expectation.ExternalDB == nil || expectation.ExternalDB.NodeID == "" || !boundedAbsolutePath(expectation.ExternalDB.CommonYAMLPath) || expectation.ExternalDB.OldAddress == "" || expectation.ExternalDB.NewAddress == "" || expectation.ExternalDB.Port <= 0 || !expectation.ExternalDB.PreserveNonAddressFields {
			return errors.New("xRocket external DB stage contract is incomplete")
		}
		if hasRecoveryArtifactRef(expectation.ExternalDB.BaselineConfig) {
			if err := validateOptionalStageRecoveryRef(expectation.ExternalDB.BaselineConfig, recoveryKindCommonYAML, expectation.ExternalDB.CommonYAMLPath); err != nil {
				return err
			}
		}
	case stageKindEtcd:
		if expectation.Etcd == nil || expectation.Etcd.NodeID == "" || !boundedAbsolutePath(expectation.Etcd.ConfigPath) || !boundedAbsolutePath(expectation.Etcd.EtcdctlPath) || expectation.Etcd.Scheme == "" || expectation.Etcd.MemberID == "" || expectation.Etcd.MemberCount != 1 || expectation.Etcd.OldClient == "" || expectation.Etcd.NewClient == "" || expectation.Etcd.ClientPort <= 0 || expectation.Etcd.OldPeer == "" || expectation.Etcd.NewPeer == "" || expectation.Etcd.PeerPort <= 0 || expectation.Etcd.ServiceName == "" || expectation.Etcd.ControlAdapter == "" {
			return errors.New("xRocket etcd stage contract is incomplete or not singleton")
		}
	case stageKindConfd:
		if expectation.Confd == nil || expectation.Confd.NodeID == "" || !boundedAbsolutePath(expectation.Confd.XrocketBinary) || len(expectation.Confd.Destinations) == 0 || expectation.Confd.OldMaster == "" || expectation.Confd.OldSlave == "" || expectation.Confd.OldVIP == "" || expectation.Confd.OldExternalDB == "" || expectation.Confd.ExpectedMaster == "" || expectation.Confd.ExpectedSlave == "" || expectation.Confd.ExpectedVIP == "" || expectation.Confd.ExpectedExternalDB == "" {
			return errors.New("xRocket confd stage contract is incomplete")
		}
		for _, destination := range expectation.Confd.Destinations {
			if !boundedAbsolutePath(destination) {
				return errors.New("xRocket confd stage contract has an unbounded destination")
			}
		}
		if len(expectation.Confd.DestinationFiles) > 0 {
			if len(expectation.Confd.DestinationFiles) != len(expectation.Confd.Destinations) {
				return errors.New("xRocket confd stage recovery baseline cardinality is invalid")
			}
			for index, destination := range expectation.Confd.Destinations {
				if err := validateOptionalStageRecoveryRef(expectation.Confd.DestinationFiles[index], recoveryKindConfdDestination, destination); err != nil {
					return err
				}
			}
		}
	case stageKindOS:
		if expectation.OS == nil || expectation.OS.NodeID == "" || expectation.OS.Interface == "" || !boundedAbsolutePath(expectation.OS.ConfigPath) || expectation.OS.OldAddress == "" || expectation.OS.NewAddress == "" || expectation.OS.PrefixLength <= 0 || expectation.OS.Gateway == "" || expectation.OS.BootIDBefore == "" || expectation.OS.Barrier != operation.StageBarrierAgentReconnect || !expectation.OS.Reboot {
			return errors.New("xRocket OS stage contract is incomplete or lacks the generic reconnect barrier")
		}
	case stageKindFinal:
		if expectation.Final == nil || expectation.Final.NodeID == "" || expectation.Final.MasterAddress == "" || expectation.Final.SlaveAddress == "" || expectation.Final.VIPAddress == "" || !expectation.Final.Nopreempt || len(expectation.Final.BusinessPorts) == 0 {
			return errors.New("xRocket final-site stage contract is incomplete")
		}
	default:
		return fmt.Errorf("unsupported xRocket expectation kind %q", expectation.Kind)
	}
	return nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

func (definition *Definition) mutateExpectation(ctx context.Context, expectation stageExpectation) (LocalMutationReceipt, error) {
	switch expectation.Kind {
	case stageKindAlias:
		return definition.mutator.AddTargetAlias(ctx, *expectation.Alias)
	case stageKindProduct:
		return definition.mutator.ReaddressProduct(ctx, *expectation.Product)
	case stageKindExternalDB:
		return definition.mutator.UpdateExternalDB(ctx, *expectation.ExternalDB)
	case stageKindEtcd:
		return definition.mutator.ReaddressEtcd(ctx, *expectation.Etcd)
	case stageKindConfd:
		return definition.mutator.RenderConfd(ctx, *expectation.Confd)
	case stageKindOS:
		return definition.mutator.CutoverOS(ctx, *expectation.OS)
	default:
		return LocalMutationReceipt{}, fmt.Errorf("xRocket stage kind %q is not mutable", expectation.Kind)
	}
}

func (definition *Definition) inspectExpectation(ctx context.Context, expectation stageExpectation) (bool, error) {
	if err := validateStageExpectation(expectation); err != nil {
		return false, err
	}
	switch expectation.Kind {
	case stageKindSnapshot:
		return true, nil
	case stageKindAlias:
		return definition.inspector.AliasSatisfied(ctx, *expectation.Alias)
	case stageKindProduct:
		return definition.inspector.ProductSatisfied(ctx, *expectation.Product)
	case stageKindExternalDB:
		return definition.inspector.ExternalDBSatisfied(ctx, *expectation.ExternalDB)
	case stageKindEtcd:
		return definition.inspector.EtcdSatisfied(ctx, *expectation.Etcd)
	case stageKindConfd:
		return definition.inspector.ConfdSatisfied(ctx, *expectation.Confd)
	case stageKindOS:
		return definition.inspector.OSSatisfied(ctx, *expectation.OS)
	case stageKindFinal:
		return definition.inspector.FinalSiteSatisfied(ctx, *expectation.Final)
	default:
		return false, fmt.Errorf("unsupported xRocket inspection kind %q", expectation.Kind)
	}
}

func validateStageLease(lease operation.LockLease, runID string, target operation.Target) error {
	if lease.OwnerID != runID {
		return errors.New("xRocket stage lease is owned by a different operation run")
	}
	key, err := operation.ResourceLockKey(target)
	if err != nil {
		return err
	}
	for _, resource := range lease.Resources {
		if resource.Key == key {
			return nil
		}
	}
	return errors.New("xRocket stage lease does not cover the local target")
}

func validateLocalMutationReceipt(receipt LocalMutationReceipt) error {
	if !validSHA256Digest(receipt.Digest) {
		return errors.New("xRocket local mutation adapter must return a bounded sha256 receipt digest")
	}
	return nil
}

func decodeApplyStageReceipt(result operation.ApplyResult) (applyStageReceipt, error) {
	if result.State.SchemaVersion != ApplyResultSchema || result.Checkpoint == "" || len(result.State.Payload) == 0 {
		return applyStageReceipt{}, errors.New("xRocket ApplyResult artifact is incomplete")
	}
	decoder := json.NewDecoder(bytes.NewReader(result.State.Payload))
	decoder.DisallowUnknownFields()
	var receipt applyStageReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return applyStageReceipt{}, fmt.Errorf("decode xRocket ApplyResult: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return applyStageReceipt{}, errors.New("xRocket ApplyResult contains trailing JSON")
		}
		return applyStageReceipt{}, err
	}
	if receipt.SchemaVersion != ApplyResultSchema || receipt.OperationID != OperationID || receipt.OperationVersion != Metadata().Version || receipt.RunID == "" || receipt.StageID == "" || receipt.NodeID == "" || receipt.RestorePointID == "" || !validSHA256Digest(receipt.RestoreManifestSHA256) {
		return applyStageReceipt{}, errors.New("xRocket ApplyResult identity is invalid")
	}
	if receipt.RestorePointID != restorePointID(receipt.RunID, receipt.NodeID, receipt.StageID) {
		return applyStageReceipt{}, errors.New("xRocket ApplyResult RestorePoint identity is invalid")
	}
	if result.Changed != (receipt.Writes && !receipt.AlreadySatisfied) {
		return applyStageReceipt{}, errors.New("xRocket ApplyResult changed flag is inconsistent with the stage receipt")
	}
	if receipt.Writes && !receipt.AlreadySatisfied && receipt.AdapterReceipt == nil {
		return applyStageReceipt{}, errors.New("xRocket changed stage receipt is missing bounded adapter evidence")
	}
	if (!receipt.Writes || receipt.AlreadySatisfied) && receipt.AdapterReceipt != nil {
		return applyStageReceipt{}, errors.New("xRocket no-op stage receipt cannot contain mutation adapter evidence")
	}
	if receipt.AdapterReceipt != nil {
		if err := validateLocalMutationReceipt(*receipt.AdapterReceipt); err != nil {
			return applyStageReceipt{}, err
		}
	}
	if err := validateStageExpectation(receipt.Expectation); err != nil {
		return applyStageReceipt{}, err
	}
	if err := validateApplyReceiptRecoveryOwner(receipt); err != nil {
		return applyStageReceipt{}, err
	}
	return receipt, nil
}

func validateReceiptCorrelation(receipt applyStageReceipt, plan executionPlan, stage operation.PlanStep, stageIndex int, spec canonicalStage, runtime operation.RuntimeInput) error {
	participants, err := restoreParticipants(plan)
	if err != nil {
		return err
	}
	expectedNode := plan.MasterNodeID
	if spec.Role == "slave" {
		expectedNode = plan.SlaveNodeID
	}
	if receipt.StageID != stage.ID || receipt.StageIndex != stageIndex || receipt.NodeID != expectedNode || receipt.Role != spec.Role || receipt.Action != stage.Action || receipt.Checkpoint != stage.Checkpoint || receipt.Writes != stage.Writes || receipt.Barrier != stage.Barrier {
		return errors.New("xRocket ApplyResult stage correlation is invalid")
	}
	if !reflect.DeepEqual(receipt.ParticipantNodeIDs, participants) {
		return errors.New("xRocket ApplyResult participant correlation is invalid")
	}
	if len(runtime.Targets) != 1 || runtime.Targets[0].NodeID != receipt.NodeID {
		return errors.New("xRocket Verify local executor correlation is invalid")
	}
	if receipt.Expectation.Kind != spec.Kind {
		return errors.New("xRocket ApplyResult expectation kind differs from stage contract")
	}
	if !stageExpectationMatchesPlan(receipt.Expectation, plan, spec, receipt.NodeID) {
		return errors.New("xRocket ApplyResult expected postcondition differs from the frozen plan")
	}
	return nil
}

func stageExpectationMatchesPlan(expectation stageExpectation, plan executionPlan, spec canonicalStage, nodeID string) bool {
	localOld := plan.Discovery.MasterAddress
	localNew := plan.Parameters.MasterTargetAddress
	if spec.Role == "slave" {
		localOld = plan.Discovery.SlaveAddress
		localNew = plan.Parameters.SlaveTargetAddress
	}
	switch spec.Kind {
	case stageKindSnapshot:
		return true
	case stageKindAlias:
		return expectation.Alias != nil && expectation.Alias.NodeID == nodeID && expectation.Alias.Role == spec.Role && expectation.Alias.OldAddress == localOld && expectation.Alias.NewAddress == localNew && expectation.Alias.Interface == plan.Discovery.Interface && expectation.Alias.PrefixLength == plan.Discovery.PrefixLength && expectation.Alias.Gateway == plan.Discovery.GatewayAddress
	case stageKindProduct:
		if expectation.Product == nil || expectation.Product.NodeID != nodeID || expectation.Product.Role != spec.Role || expectation.Product.OldMaster != plan.Discovery.MasterAddress || expectation.Product.OldSlave != plan.Discovery.SlaveAddress || expectation.Product.OldVIP != plan.Discovery.VIPAddress || expectation.Product.NewMaster != plan.Parameters.MasterTargetAddress || expectation.Product.NewSlave != plan.Parameters.SlaveTargetAddress || expectation.Product.NewVIP != plan.Parameters.VIPTargetAddress {
			return false
		}
		if hasRecoveryArtifactRef(expectation.Product.ProductBundle) {
			if expectation.Product.ProductBundle.SourcePath != plan.Discovery.ProductVersionEvidence || expectation.Product.KeepalivedConfig.SourcePath != plan.Discovery.KeepalivedConfigPath {
				return false
			}
		}
		return true
	case stageKindExternalDB:
		return expectation.ExternalDB != nil && expectation.ExternalDB.NodeID == nodeID && expectation.ExternalDB.Role == spec.Role && expectation.ExternalDB.OldAddress == plan.MasterProfile.ExternalDBAddress && expectation.ExternalDB.NewAddress == plan.Parameters.ExternalDBTargetAddress && expectation.ExternalDB.Port == plan.MasterProfile.ExternalDBPort && expectation.ExternalDB.PreserveNonAddressFields && (!hasRecoveryArtifactRef(expectation.ExternalDB.BaselineConfig) || expectation.ExternalDB.BaselineConfig.SourcePath == plan.MasterProfile.CommonYAMLPath)
	case stageKindEtcd:
		return expectation.Etcd != nil && expectation.Etcd.NodeID == nodeID && expectation.Etcd.Role == spec.Role && expectation.Etcd.OldClient == localOld && expectation.Etcd.OldPeer == localOld && expectation.Etcd.NewClient == localNew && expectation.Etcd.NewPeer == localNew && expectation.Etcd.MemberID != "" && expectation.Etcd.MemberCount == 1
	case stageKindConfd:
		if expectation.Confd == nil || expectation.Confd.NodeID != nodeID || expectation.Confd.Role != spec.Role || expectation.Confd.OldMaster != plan.Discovery.MasterAddress || expectation.Confd.OldSlave != plan.Discovery.SlaveAddress || expectation.Confd.OldVIP != plan.Discovery.VIPAddress || expectation.Confd.OldExternalDB != plan.MasterProfile.ExternalDBAddress || expectation.Confd.ExpectedMaster != plan.Parameters.MasterTargetAddress || expectation.Confd.ExpectedSlave != plan.Parameters.SlaveTargetAddress || expectation.Confd.ExpectedVIP != plan.Parameters.VIPTargetAddress || expectation.Confd.ExpectedExternalDB != plan.Parameters.ExternalDBTargetAddress {
			return false
		}
		if len(expectation.Confd.DestinationFiles) > 0 {
			if len(expectation.Confd.DestinationFiles) != len(expectation.Confd.Destinations) {
				return false
			}
			for index, destination := range expectation.Confd.Destinations {
				if expectation.Confd.DestinationFiles[index].SourcePath != destination {
					return false
				}
			}
		}
		return true
	case stageKindOS:
		return expectation.OS != nil && expectation.OS.NodeID == nodeID && expectation.OS.Role == spec.Role && expectation.OS.OldAddress == localOld && expectation.OS.NewAddress == localNew && expectation.OS.Interface == plan.Discovery.Interface && expectation.OS.PrefixLength == plan.Discovery.PrefixLength && expectation.OS.Gateway == plan.Discovery.GatewayAddress && expectation.OS.Barrier == operation.StageBarrierAgentReconnect && expectation.OS.Reboot
	case stageKindFinal:
		if expectation.Final == nil {
			return false
		}
		ports := append([]int(nil), expectation.Final.BusinessPorts...)
		expectedPorts := append([]int(nil), plan.Discovery.BusinessPorts...)
		sort.Ints(ports)
		sort.Ints(expectedPorts)
		return expectation.Final.NodeID == nodeID && expectation.Final.Role == spec.Role && expectation.Final.MasterAddress == plan.Parameters.MasterTargetAddress && expectation.Final.SlaveAddress == plan.Parameters.SlaveTargetAddress && expectation.Final.VIPAddress == plan.Parameters.VIPTargetAddress && expectation.Final.Nopreempt && reflect.DeepEqual(ports, expectedPorts)
	default:
		return false
	}
}

func boundedAbsolutePath(value string) bool {
	return value != "" && len(value) <= 512 && strings.HasPrefix(value, "/") && path.Clean(value) == value
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
