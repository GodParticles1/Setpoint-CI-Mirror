package sysctlrepair

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/plugins/sysctlconfig"
)

const ID = "linux.network.icmp_redirects.runtime_repair"

const (
	planSchema  = "setpoint.sysctl_runtime_repair.plan.v1"
	stateSchema = "setpoint.sysctl_runtime_repair.state.v1"
)

var allowedChecks = map[string]string{
	"net.ipv4.conf.all.accept_redirects.persisted":     "net.ipv4.conf.all.accept_redirects",
	"net.ipv4.conf.default.accept_redirects.persisted": "net.ipv4.conf.default.accept_redirects",
	"net.ipv4.conf.all.send_redirects.persisted":       "net.ipv4.conf.all.send_redirects",
	"net.ipv4.conf.default.send_redirects.persisted":   "net.ipv4.conf.default.send_redirects",
}

var checkOptions = []string{
	"net.ipv4.conf.all.accept_redirects.persisted",
	"net.ipv4.conf.all.send_redirects.persisted",
	"net.ipv4.conf.default.accept_redirects.persisted",
	"net.ipv4.conf.default.send_redirects.persisted",
}

type parameters struct {
	CheckID     string `json:"check_id"`
	TargetValue string `json:"target_value"`
}

type observedState struct {
	CheckID         string `json:"check_id"`
	Key             string `json:"key"`
	RuntimeValue    string `json:"runtime_value"`
	PersistedValue  string `json:"persisted_value"`
	PersistedDigest string `json:"persisted_digest"`
}

type planPayload struct {
	CheckID         string `json:"check_id"`
	Key             string `json:"key"`
	TargetValue     string `json:"target_value"`
	BeforeRuntime   string `json:"before_runtime"`
	PersistedDigest string `json:"persisted_digest"`
}

type Definition struct {
	executor executor.CommandExecutor
}

func NewDefinition(commandExecutor executor.CommandExecutor) (*Definition, error) {
	if commandExecutor == nil {
		return nil, errors.New("sysctl repair executor is required")
	}
	return &Definition{executor: commandExecutor}, nil
}

func Metadata() operation.Metadata {
	return operation.Metadata{
		ID: ID, Category: "Linux 运行时修复", Name: "ICMP Redirect 运行时修复", Version: "1.0.0",
		Description: "仅在持久化配置已明确解析为 0 时，将指定 ICMP Redirect 运行时 sysctl 修复为 0。",
		Risk:        operation.RiskLow, Impact: "仅修改一个固定的运行时布尔 sysctl；已验证的持久化配置保持不变。",
		SupportedSystems: []string{"linux"},
		Parameters: []operation.Parameter{
			{Name: "check_id", Type: "string", Description: "要修复的持久化 ICMP Redirect 检查项", Required: true, Options: append([]string(nil), checkOptions...)},
			{Name: "target_value", Type: "string", Description: "本次检查冻结的目标值", Required: true, Options: []string{"runtime=0; persisted=0"}},
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

func decodeParameters(raw json.RawMessage) (parameters, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value parameters
	if err := decoder.Decode(&value); err != nil {
		return parameters{}, fmt.Errorf("decode sysctl repair parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return parameters{}, errors.New("decode sysctl repair parameters: trailing JSON value")
	}
	value.CheckID = strings.TrimSpace(value.CheckID)
	value.TargetValue = strings.TrimSpace(value.TargetValue)
	if _, ok := allowedChecks[value.CheckID]; !ok {
		return parameters{}, fmt.Errorf("unsupported ICMP Redirect check_id %q", value.CheckID)
	}
	if value.TargetValue != "runtime=0; persisted=0" {
		return parameters{}, fmt.Errorf("target_value must equal the existing Check recommendation %q", "runtime=0; persisted=0")
	}
	return value, nil
}

func (definition *Definition) Discover(ctx context.Context, input operation.DiscoverInput) (operation.Discovery, error) {
	value, err := decodeParameters(input.Runtime.Parameters)
	if err != nil {
		return operation.Discovery{}, err
	}
	if input.Runtime.System != "linux" {
		return operation.Discovery{Applicable: false, Summary: "ICMP Redirect repair is Linux-only"}, nil
	}
	state, err := definition.observe(ctx, value)
	if err != nil {
		return operation.Discovery{}, err
	}
	artifact, err := stateArtifact(state)
	if err != nil {
		return operation.Discovery{}, err
	}
	return operation.Discovery{
		Applicable: true, Summary: "Observed fixed runtime sysctl and authoritative supported persistent loading views",
		Targets: append([]operation.Target(nil), input.Runtime.Targets...), Snapshot: artifact,
	}, nil
}

func (definition *Definition) Precheck(ctx context.Context, input operation.PrecheckInput) (operation.Precheck, error) {
	value, err := decodeParameters(input.Runtime.Parameters)
	if err != nil {
		return operation.Precheck{}, err
	}
	state, err := definition.observe(ctx, value)
	if err != nil {
		return operation.Precheck{}, err
	}
	artifact, err := stateArtifact(state)
	if err != nil {
		return operation.Precheck{}, err
	}
	if state.PersistedValue != "0" {
		return operation.Precheck{Passed: false, Summary: "Persistent sysctl is not already proven safe", Snapshot: artifact,
			Findings: []operation.Finding{{Code: "PERSISTED_SYSCTL_NOT_SAFE", Severity: operation.FindingBlocking, Summary: "Automatic repair requires persistent value 0 before runtime mutation"}}}, nil
	}
	if state.RuntimeValue == "0" {
		return operation.Precheck{Passed: false, Summary: "Runtime value already matches the recommendation", Snapshot: artifact,
			Findings: []operation.Finding{{Code: "RUNTIME_ALREADY_COMPLIANT", Severity: operation.FindingBlocking, Summary: "No mutation is required"}}}, nil
	}
	return operation.Precheck{Passed: true, Summary: "Runtime value can be repaired without changing persistent configuration", Snapshot: artifact}, nil
}

func (definition *Definition) Plan(_ context.Context, input operation.PlanInput) (operation.Plan, error) {
	value, err := decodeParameters(input.Runtime.Parameters)
	if err != nil {
		return operation.Plan{}, err
	}
	state, err := decodeState(input.Precheck.Snapshot)
	if err != nil {
		return operation.Plan{}, err
	}
	if !input.Precheck.Passed || state.CheckID != value.CheckID || state.PersistedValue != "0" || state.RuntimeValue == "0" {
		return operation.Plan{}, errors.New("sysctl repair plan requires a passed, correlated precheck with persisted=0 and unsafe runtime value")
	}
	payload := planPayload{CheckID: state.CheckID, Key: state.Key, TargetValue: "0", BeforeRuntime: state.RuntimeValue, PersistedDigest: state.PersistedDigest}
	artifact, err := encodeArtifact(planSchema, payload)
	if err != nil {
		return operation.Plan{}, err
	}
	return operation.Plan{
		SchemaVersion: "setpoint.operation.plan.v1", Summary: fmt.Sprintf("Set runtime %s from %s to 0", state.Key, state.RuntimeValue),
		Steps:     []operation.PlanStep{{ID: "set-runtime-sysctl", Name: "Set fixed runtime sysctl", Target: input.Runtime.Targets[0], Action: "sysctl_set_runtime", Checkpoint: "runtime_sysctl_set", Writes: true, RetrySafe: true, RollbackAction: "restore_runtime_sysctl"}},
		Execution: artifact,
	}, nil
}

func (definition *Definition) Impact(_ context.Context, input operation.ImpactInput) (operation.Impact, error) {
	plan, err := decodePlan(input.Plan.Execution)
	if err != nil {
		return operation.Impact{}, err
	}
	return operation.Impact{
		Summary:          "Change one fixed ICMP Redirect runtime boolean while leaving proven-safe persistence untouched",
		Risk:             operation.RiskLow,
		Changes:          []operation.Change{{Target: input.Runtime.Targets[0], Before: plan.BeforeRuntime, After: "0", Risk: "bounded runtime sysctl change"}},
		RequiresDowntime: false, RequiresWriteFence: false, EstimatedDuration: time.Second,
	}, nil
}

func (definition *Definition) Apply(ctx context.Context, input operation.ApplyInput) (operation.ApplyResult, error) {
	if err := input.Lease.Validate(time.Now().UTC()); err != nil {
		return operation.ApplyResult{}, err
	}
	plan, err := decodePlan(input.Plan.Execution)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	before, err := definition.observe(ctx, parameters{CheckID: plan.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.ApplyResult{}, err
	}
	if before.PersistedValue != "0" || before.PersistedDigest != plan.PersistedDigest {
		return operation.ApplyResult{}, errors.New("persistent sysctl evidence changed after planning; refusing runtime mutation")
	}
	if before.RuntimeValue != plan.BeforeRuntime && before.RuntimeValue != "0" {
		return operation.ApplyResult{}, errors.New("runtime sysctl changed after planning; refusing stale mutation")
	}
	if before.RuntimeValue != "0" {
		if _, err := definition.executor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-w", plan.Key + "=0"}}); err != nil {
			return operation.ApplyResult{}, fmt.Errorf("set runtime %s=0: %w", plan.Key, err)
		}
	}
	state, err := definition.observe(ctx, parameters{CheckID: plan.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.ApplyResult{}, err
	}
	artifact, err := stateArtifact(state)
	if err != nil {
		return operation.ApplyResult{}, err
	}
	return operation.ApplyResult{Changed: before.RuntimeValue != "0", Checkpoint: "runtime_sysctl_set", State: artifact}, nil
}

func (definition *Definition) Verify(ctx context.Context, input operation.VerifyInput) (operation.Verification, error) {
	plan, err := decodePlan(input.Plan.Execution)
	if err != nil {
		return operation.Verification{}, err
	}
	state, err := definition.observe(ctx, parameters{CheckID: plan.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.Verification{}, err
	}
	passed := state.RuntimeValue == "0" && state.PersistedValue == "0" && state.PersistedDigest == plan.PersistedDigest
	summary := "runtime and persistent ICMP Redirect values match the frozen recommendation"
	if !passed {
		summary = "runtime or persistent ICMP Redirect evidence changed after Apply"
	}
	return operation.Verification{Passed: passed, Summary: summary}, nil
}

func (definition *Definition) Rollback(ctx context.Context, input operation.RollbackInput) (operation.RollbackResult, error) {
	if err := input.Lease.Validate(time.Now().UTC()); err != nil {
		return operation.RollbackResult{}, err
	}
	manifest, err := decodeRestoreManifest(input.RestorePoint.Manifest)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	if _, err := definition.executor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-w", manifest.Key + "=" + manifest.RuntimeValue}}); err != nil {
		return operation.RollbackResult{}, fmt.Errorf("restore runtime %s=%s: %w", manifest.Key, manifest.RuntimeValue, err)
	}
	state, err := definition.observe(ctx, parameters{CheckID: manifest.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.RollbackResult{}, err
	}
	artifact, err := stateArtifact(state)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	return operation.RollbackResult{Restored: true, Checkpoint: "runtime_sysctl_restored", State: artifact}, nil
}

func (definition *Definition) VerifyRollback(ctx context.Context, input operation.VerifyRollbackInput) (operation.Verification, error) {
	manifest, err := decodeRestoreManifest(input.RestorePoint.Manifest)
	if err != nil {
		return operation.Verification{}, err
	}
	state, err := definition.observe(ctx, parameters{CheckID: manifest.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.Verification{}, err
	}
	passed := state.RuntimeValue == manifest.RuntimeValue && state.PersistedValue == "0" && state.PersistedDigest == manifest.PersistedDigest
	summary := "runtime sysctl restored to the verified restore point"
	if !passed {
		summary = "runtime rollback verification did not match the restore point"
	}
	return operation.Verification{Passed: passed, Summary: summary}, nil
}

func (definition *Definition) observe(ctx context.Context, value parameters) (observedState, error) {
	key := allowedChecks[value.CheckID]
	result, err := definition.executor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-n", key}})
	if err != nil {
		return observedState{}, fmt.Errorf("read runtime %s: %w", key, err)
	}
	if result.StdoutTruncated {
		return observedState{}, errors.New("runtime sysctl output was truncated")
	}
	runtimeValue := strings.TrimSpace(result.Stdout)
	if runtimeValue != "0" && runtimeValue != "1" {
		return observedState{}, fmt.Errorf("runtime sysctl returned unsupported boolean value %q", runtimeValue)
	}
	snapshot, err := sysctlconfig.Collect(ctx, definition.executor)
	if err != nil {
		return observedState{}, fmt.Errorf("collect persistent sysctl sources: %w", err)
	}
	resolution := snapshot.Resolve(key)
	if resolution.State != sysctlconfig.StateResolved {
		return observedState{}, fmt.Errorf("persistent sysctl evidence is not uniquely resolved: %s", resolution.Reason)
	}
	if resolution.Value != "0" && resolution.Value != "1" {
		return observedState{}, fmt.Errorf("persistent sysctl returned unsupported boolean value %q", resolution.Value)
	}
	return observedState{CheckID: value.CheckID, Key: key, RuntimeValue: runtimeValue, PersistedValue: resolution.Value, PersistedDigest: resolution.Digest}, nil
}

func stateArtifact(state observedState) (operation.Artifact, error) {
	return encodeArtifact(stateSchema, state)
}

func decodeState(artifact operation.Artifact) (observedState, error) {
	if artifact.SchemaVersion != stateSchema {
		return observedState{}, fmt.Errorf("unsupported sysctl state schema %q", artifact.SchemaVersion)
	}
	var state observedState
	if err := json.Unmarshal(artifact.Payload, &state); err != nil {
		return observedState{}, err
	}
	return state, nil
}

func decodePlan(artifact operation.Artifact) (planPayload, error) {
	if artifact.SchemaVersion != planSchema {
		return planPayload{}, fmt.Errorf("unsupported sysctl repair plan schema %q", artifact.SchemaVersion)
	}
	var plan planPayload
	if err := json.Unmarshal(artifact.Payload, &plan); err != nil {
		return planPayload{}, err
	}
	if allowedChecks[plan.CheckID] != plan.Key || plan.TargetValue != "0" || plan.BeforeRuntime == "" || plan.PersistedDigest == "" {
		return planPayload{}, errors.New("sysctl repair plan payload is invalid")
	}
	return plan, nil
}

func encodeArtifact(schema string, value any) (operation.Artifact, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return operation.Artifact{}, err
	}
	return operation.Artifact{SchemaVersion: schema, Payload: payload}, nil
}
