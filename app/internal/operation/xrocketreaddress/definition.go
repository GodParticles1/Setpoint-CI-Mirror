package xrocketreaddress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const applyMechanismGap = "evidence-backed xRocket readdress Apply/rollback execution is not yet registered on the Agent"

var errApplyMechanismUnverified = errors.New(applyMechanismGap)

type profileDiscoverer func(context.Context, discoveryState) (runtimeProfile, error)

type Definition struct {
	probe             discoveryProbe
	profileDiscover   profileDiscoverer
	mutator           LocalMutationAdapter
	inspector         LocalInspectionAdapter
	rollbackMutator   LocalRollbackMutationAdapter
	rollbackInspector LocalRollbackInspectionAdapter
}

func NewDefinition(commandExecutor executor.CommandExecutor) (*Definition, error) {
	if commandExecutor == nil {
		return nil, errors.New("xRocket readdress executor is required")
	}
	probe := discoveryProbe{executor: commandExecutor}
	return &Definition{probe: probe, profileDiscover: probe.discoverRuntimeProfile}, nil
}

// NewDefinitionWithStageAdapters constructs the bounded Apply/Verify core for
// direct execution-contract tests and future reviewed Agent-local composition.
// Production composition deliberately continues to use NewDefinition, which
// keeps xRocket writes fail-closed until a separately verified mutation adapter
// is registered by an explicitly authorized checkpoint.
func NewDefinitionWithStageAdapters(commandExecutor executor.CommandExecutor, mutator LocalMutationAdapter, inspector LocalInspectionAdapter) (*Definition, error) {
	if mutator == nil || inspector == nil {
		return nil, errors.New("xRocket stage mutation and inspection adapters are required")
	}
	definition, err := NewDefinition(commandExecutor)
	if err != nil {
		return nil, err
	}
	definition.mutator = mutator
	definition.inspector = inspector
	if rollbackMutator, ok := mutator.(LocalRollbackMutationAdapter); ok {
		definition.rollbackMutator = rollbackMutator
	}
	if rollbackInspector, ok := inspector.(LocalRollbackInspectionAdapter); ok {
		definition.rollbackInspector = rollbackInspector
	}
	return definition, nil
}

func Metadata() operation.Metadata {
	return operation.Metadata{
		ID: OperationID, Category: "xRocket 站点运维", Name: "xRocket 站点地址变更", Version: "1.2.0",
		Description:      "自动发现 C68/R4 双机当前地址、VIP、网络、产品与数据库连接证据；用户只提供新 Master、Slave、VIP 和外置数据库目标 IP。",
		Risk:             operation.RiskCritical,
		Impact:           "站点地址变更会中断 Agent 连接，并影响 VIP、HA、etcd、产品配置、外置数据库连接目标与业务可达性。",
		SupportedSystems: []string{"linux"},
		Parameters: []operation.Parameter{
			{Name: "master_target_address", Type: "string", Description: "Master 节点目标 IPv4 地址", Required: true},
			{Name: "slave_target_address", Type: "string", Description: "Slave 节点目标 IPv4 地址", Required: true},
			{Name: "vip_target_address", Type: "string", Description: "站点 VIP 目标 IPv4 地址", Required: true},
			{Name: "external_db_target_address", Type: "string", Description: "外置数据库目标 IPv4 地址；现有 xRocket 访问端口、用户、密码、database 与 schema 保持不变", Required: true},
		},
	}
}

func (definition *Definition) Metadata() operation.Metadata { return Metadata() }

func (definition *Definition) NormalizeParameters(raw json.RawMessage) (json.RawMessage, error) {
	value, err := decodeParameters(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (definition *Definition) Discover(ctx context.Context, input operation.DiscoverInput) (operation.Discovery, error) {
	if _, err := decodeParameters(input.Runtime.Parameters); err != nil {
		return operation.Discovery{}, err
	}
	if input.Runtime.System != "linux" {
		return operation.Discovery{Applicable: false, Summary: "xRocket site readdress discovery is Linux-only"}, nil
	}
	nodeID := ""
	for _, target := range input.Runtime.Targets {
		if target.Kind == operation.TargetNode && target.NodeID != "" {
			nodeID = target.NodeID
			break
		}
	}
	if nodeID == "" {
		return operation.Discovery{}, errors.New("xRocket readdress requires a node target for Agent-local discovery")
	}
	state, err := definition.probe.discover(ctx, nodeID)
	if err != nil {
		return operation.Discovery{}, err
	}
	artifact, err := encodeArtifact(discoverySchema, state)
	if err != nil {
		return operation.Discovery{}, err
	}
	findings := make([]operation.Finding, 0, len(state.Unresolved))
	for _, unresolved := range state.Unresolved {
		findings = append(findings, operation.Finding{
			Code: "DISCOVERY_EVIDENCE_UNRESOLVED", Severity: operation.FindingBlocking,
			Summary: "xRocket site evidence is incomplete", Detail: unresolved,
		})
	}
	summary := fmt.Sprintf("Discovered xRocket address evidence on node %s", nodeID)
	if !state.resolved() {
		summary = fmt.Sprintf("xRocket address discovery on node %s is incomplete", nodeID)
	}
	return operation.Discovery{
		Applicable: state.resolved(), Summary: summary, Targets: append([]operation.Target(nil), input.Runtime.Targets...),
		Snapshot: artifact, Findings: findings,
	}, nil
}

func (definition *Definition) Precheck(ctx context.Context, input operation.PrecheckInput) (operation.Precheck, error) {
	value, err := decodeParameters(input.Runtime.Parameters)
	if err != nil {
		return operation.Precheck{}, err
	}
	state, err := decodeDiscovery(input.Discovery.Snapshot)
	if err != nil {
		return operation.Precheck{}, err
	}
	masterNodeID, slaveNodeID, err := nodeParticipants(input.Runtime.Targets, state.NodeID)
	if err != nil {
		return operation.Precheck{
			Passed: false, Summary: "xRocket readdress requires two registered Agent participants before planning", Snapshot: input.Discovery.Snapshot,
			Findings: []operation.Finding{{Code: "TWO_PARTICIPANTS_REQUIRED", Severity: operation.FindingBlocking, Summary: "Exactly two Agent node participants are required", Detail: err.Error()}},
		}, nil
	}
	if definition.profileDiscover == nil {
		return operation.Precheck{}, errors.New("xRocket runtime profile discoverer is unavailable")
	}
	profile, err := definition.profileDiscover(ctx, state)
	if err != nil {
		return operation.Precheck{
			Passed: false, Summary: "xRocket C68 runtime profile could not be proven", Snapshot: input.Discovery.Snapshot,
			Findings: []operation.Finding{{Code: "RUNTIME_PROFILE_UNVERIFIED", Severity: operation.FindingBlocking, Summary: "Runtime profile evidence is incomplete", Detail: err.Error(), Target: &operation.Target{Kind: operation.TargetNode, NodeID: masterNodeID}}},
		}, nil
	}
	if err := validateBoundedPrecheck(value, state, profile); err != nil {
		return operation.Precheck{
			Passed: false, Summary: "xRocket readdress is outside the bounded C68/R4 execution envelope", Snapshot: input.Discovery.Snapshot,
			Findings: []operation.Finding{{Code: "BOUNDED_CONTRACT_REJECTED", Severity: operation.FindingBlocking, Summary: "Precheck rejected the discovered topology", Detail: err.Error(), Target: &operation.Target{Kind: operation.TargetNode, NodeID: masterNodeID}}},
		}, nil
	}
	precheck := precheckState{SchemaVersion: precheckSchema, Parameters: value, Discovery: state, MasterProfile: profile, MasterNodeID: masterNodeID, SlaveNodeID: slaveNodeID}
	artifact, err := encodeArtifact(precheckSchema, precheck)
	if err != nil {
		return operation.Precheck{}, err
	}
	return operation.Precheck{
		Passed:   true,
		Summary:  "Current Master, bounded C68/R4 runtime profile, singleton etcd, ifcfg network, nopreempt HA and four target inputs are validated; Slave profile remains a mandatory snapshot-first stage before mutation",
		Snapshot: artifact,
	}, nil
}

func (*Definition) Plan(_ context.Context, input operation.PlanInput) (operation.Plan, error) {
	state, err := decodePrecheck(input.Precheck.Snapshot)
	if err != nil {
		return operation.Plan{}, err
	}
	if !input.Precheck.Passed {
		return operation.Plan{}, errors.New("xRocket plan requires a passed precheck")
	}
	return buildPlan(state)
}

func (*Definition) Impact(_ context.Context, input operation.ImpactInput) (operation.Impact, error) {
	plan, err := decodeExecutionPlan(input.Plan)
	if err != nil {
		return operation.Impact{}, err
	}
	return buildImpact(plan), nil
}

func (definition *Definition) Apply(ctx context.Context, input operation.ApplyInput) (operation.ApplyResult, error) {
	if definition.mutator == nil || definition.inspector == nil {
		return definition.applyStage(ctx, input)
	}
	plan, err := decodeExecutionPlan(input.Plan)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	if !reflect.DeepEqual(input.Impact, buildImpact(plan)) {
		return operation.ApplyResult{}, errors.New("xRocket Apply impact differs from the frozen execution plan")
	}
	result, err := definition.applyStage(ctx, input)
	if err != nil {
		return result, err
	}
	if input.Stage != nil && input.Stage.ID == "final-slave" {
		verification, verifyErr := definition.verifyStage(ctx, operation.VerifyInput{Runtime: input.Runtime, Plan: input.Plan, Stage: input.Stage, Apply: result})
		if verifyErr != nil {
			return operation.ApplyResult{}, verifyErr
		}
		if !verification.Passed {
			return operation.ApplyResult{}, errors.New("xRocket final-site postcondition does not match the frozen bounded contract")
		}
	}
	return result, nil
}

func (definition *Definition) Verify(ctx context.Context, input operation.VerifyInput) (operation.Verification, error) {
	if definition.inspector == nil {
		return definition.verifyStage(ctx, input)
	}
	receipt, err := decodeApplyStageReceipt(input.Apply)
	if err != nil {
		return operation.Verification{}, err
	}
	if input.Apply.Checkpoint != receipt.Checkpoint {
		return operation.Verification{}, errors.New("xRocket ApplyResult checkpoint differs from the typed stage receipt")
	}
	return definition.verifyStage(ctx, input)
}

func (definition *Definition) Rollback(ctx context.Context, input operation.RollbackInput) (operation.RollbackResult, error) {
	if definition.rollbackMutator == nil || definition.rollbackInspector == nil {
		return operation.RollbackResult{}, errApplyMechanismUnverified
	}
	return definition.rollbackStage(ctx, input)
}

func (definition *Definition) VerifyRollback(ctx context.Context, input operation.VerifyRollbackInput) (operation.Verification, error) {
	if definition.rollbackInspector == nil {
		return operation.Verification{}, errApplyMechanismUnverified
	}
	return definition.verifyRollbackStage(ctx, input)
}

func decodeDiscovery(artifact operation.Artifact) (discoveryState, error) {
	if artifact.SchemaVersion != discoverySchema {
		return discoveryState{}, fmt.Errorf("unsupported xRocket discovery schema %q", artifact.SchemaVersion)
	}
	var state discoveryState
	if err := json.Unmarshal(artifact.Payload, &state); err != nil {
		return discoveryState{}, fmt.Errorf("decode xRocket discovery: %w", err)
	}
	if state.SchemaVersion != discoverySchema || state.NodeID == "" {
		return discoveryState{}, errors.New("xRocket discovery correlation is invalid")
	}
	return state, nil
}

func encodeArtifact(schema string, value any) (operation.Artifact, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return operation.Artifact{}, err
	}
	return operation.Artifact{SchemaVersion: schema, Payload: payload}, nil
}

var _ operation.OperationDefinition = (*Definition)(nil)
