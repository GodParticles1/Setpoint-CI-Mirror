package xrocketreaddress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const applyMechanismGap = "evidence does not establish a versioned, reversible xRocket readdress Apply mechanism or safe master/slave/VIP mutation order"

var errApplyMechanismUnverified = errors.New(applyMechanismGap)

type Definition struct {
	probe discoveryProbe
}

func NewDefinition(commandExecutor executor.CommandExecutor) (*Definition, error) {
	if commandExecutor == nil {
		return nil, errors.New("xRocket readdress executor is required")
	}
	return &Definition{probe: discoveryProbe{executor: commandExecutor}}, nil
}

func Metadata() operation.Metadata {
	return operation.Metadata{
		ID: OperationID, Category: "xRocket 站点运维", Name: "xRocket 站点地址变更", Version: "1.1.0",
		Description:      "自动发现 xRocket 双机地址、VIP、网络和版本证据；真实修改机制未闭合时拒绝执行。",
		Risk:             operation.RiskCritical,
		Impact:           "站点地址变更会中断 Agent 连接，并可能影响 VIP、HA、监督进程、产品配置与业务可达性。",
		SupportedSystems: []string{"linux"},
		Parameters: []operation.Parameter{
			{Name: "master_target_address", Type: "string", Description: "Master 节点目标 IPv4 地址", Required: true},
			{Name: "slave_target_address", Type: "string", Description: "Slave 节点目标 IPv4 地址", Required: true},
			{Name: "vip_target_address", Type: "string", Description: "站点 VIP 目标 IPv4 地址", Required: true},
			{Name: "prefix_length", Type: "string", Description: "目标网络前缀长度（1-32）", Required: true},
			{Name: "gateway_address", Type: "string", Description: "目标默认网关 IPv4 地址", Required: true},
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

func (definition *Definition) Precheck(_ context.Context, input operation.PrecheckInput) (operation.Precheck, error) {
	state, err := decodeDiscovery(input.Discovery.Snapshot)
	if err != nil {
		return operation.Precheck{}, err
	}
	return operation.Precheck{
		Passed:   false,
		Summary:  "xRocket readdress Apply is blocked because the product mutation and recovery mechanism is not evidence-closed",
		Snapshot: input.Discovery.Snapshot,
		Findings: []operation.Finding{{
			Code: "APPLY_MECHANISM_UNVERIFIED", Severity: operation.FindingBlocking,
			Summary: "No evidence-backed Apply mechanism is available", Detail: applyMechanismGap,
			Target: &operation.Target{Kind: operation.TargetNode, NodeID: state.NodeID},
		}},
	}, nil
}

func (*Definition) Plan(context.Context, operation.PlanInput) (operation.Plan, error) {
	return operation.Plan{}, errApplyMechanismUnverified
}

func (*Definition) Impact(context.Context, operation.ImpactInput) (operation.Impact, error) {
	return operation.Impact{}, errApplyMechanismUnverified
}

func (*Definition) Apply(context.Context, operation.ApplyInput) (operation.ApplyResult, error) {
	return operation.ApplyResult{}, errApplyMechanismUnverified
}

func (*Definition) Verify(context.Context, operation.VerifyInput) (operation.Verification, error) {
	return operation.Verification{}, errApplyMechanismUnverified
}

func (*Definition) Rollback(context.Context, operation.RollbackInput) (operation.RollbackResult, error) {
	return operation.RollbackResult{}, errApplyMechanismUnverified
}

func (*Definition) VerifyRollback(context.Context, operation.VerifyRollbackInput) (operation.Verification, error) {
	return operation.Verification{}, errApplyMechanismUnverified
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
