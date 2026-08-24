package sysctlrepair

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const (
	restoreProviderID = "setpoint.sysctl_runtime.v1"
	restoreSchema     = "setpoint.sysctl_runtime.restore.v1"
)

type restoreManifest struct {
	CheckID         string `json:"check_id"`
	Key             string `json:"key"`
	RuntimeValue    string `json:"runtime_value"`
	PersistedDigest string `json:"persisted_digest"`
}

type RestoreProvider struct {
	definition *Definition
	now        func() time.Time
}

func NewRestoreProvider(commandExecutor executor.CommandExecutor) (*RestoreProvider, error) {
	definition, err := NewDefinition(commandExecutor)
	if err != nil {
		return nil, err
	}
	return &RestoreProvider{definition: definition, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (provider *RestoreProvider) ID() string { return restoreProviderID }

func (provider *RestoreProvider) Create(ctx context.Context, request operation.RestorePointRequest) (operation.RestorePoint, error) {
	if request.OperationID != ID {
		return operation.RestorePoint{}, fmt.Errorf("restore provider does not support operation %q", request.OperationID)
	}
	plan, err := decodePlan(request.Plan.Execution)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	state, err := provider.definition.observe(ctx, parameters{CheckID: plan.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.RestorePoint{}, err
	}
	if state.PersistedValue != "0" || state.PersistedDigest != plan.PersistedDigest {
		return operation.RestorePoint{}, errors.New("persistent sysctl evidence changed before restore point creation")
	}
	manifest := restoreManifest{CheckID: state.CheckID, Key: state.Key, RuntimeValue: state.RuntimeValue, PersistedDigest: state.PersistedDigest}
	artifact, err := encodeArtifact(restoreSchema, manifest)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	createdAt := provider.now()
	digest := sha256.Sum256([]byte(request.RunID + "\x00" + state.Key))
	point := operation.RestorePoint{
		ID: "sysctl-rp-" + hex.EncodeToString(digest[:])[:16], ProviderID: restoreProviderID,
		OperationID: request.OperationID, RunID: request.RunID, Status: operation.RestorePointVerified,
		Targets: append([]operation.Target(nil), request.Targets...), CreatedAt: createdAt, Manifest: artifact,
	}
	if request.Retention > 0 {
		expires := createdAt.Add(request.Retention)
		point.ExpiresAt = &expires
	}
	if err := operation.ValidateRestorePoint(point, createdAt); err != nil {
		return operation.RestorePoint{}, err
	}
	return point, nil
}

func (provider *RestoreProvider) Verify(ctx context.Context, point operation.RestorePoint) (operation.Verification, error) {
	manifest, err := decodeRestoreManifest(point.Manifest)
	if err != nil {
		return operation.Verification{}, err
	}
	if point.ProviderID != restoreProviderID || point.OperationID != ID {
		return operation.Verification{}, errors.New("restore point provider or operation identity is invalid")
	}
	state, err := provider.definition.observe(ctx, parameters{CheckID: manifest.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.Verification{}, err
	}
	passed := state.Key == manifest.Key && state.RuntimeValue == manifest.RuntimeValue && state.PersistedValue == "0" && state.PersistedDigest == manifest.PersistedDigest
	return operation.Verification{Passed: passed, Summary: verificationSummary(passed, "restore point matches the current runtime and persistent evidence", "restore point no longer matches current sysctl evidence")}, nil
}

func (provider *RestoreProvider) Restore(ctx context.Context, point operation.RestorePoint, _ operation.ApplyResult) (operation.RollbackResult, error) {
	manifest, err := decodeRestoreManifest(point.Manifest)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	if _, err := provider.definition.executor.Execute(ctx, executor.Command{Name: "sysctl", Args: []string{"-w", manifest.Key + "=" + manifest.RuntimeValue}}); err != nil {
		return operation.RollbackResult{}, err
	}
	state, err := provider.definition.observe(ctx, parameters{CheckID: manifest.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.RollbackResult{}, err
	}
	artifact, err := stateArtifact(state)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	return operation.RollbackResult{Restored: true, Checkpoint: "runtime_sysctl_restored", State: artifact}, nil
}

func (provider *RestoreProvider) VerifyRestored(ctx context.Context, point operation.RestorePoint, _ operation.RollbackResult) (operation.Verification, error) {
	manifest, err := decodeRestoreManifest(point.Manifest)
	if err != nil {
		return operation.Verification{}, err
	}
	state, err := provider.definition.observe(ctx, parameters{CheckID: manifest.CheckID, TargetValue: "runtime=0; persisted=0"})
	if err != nil {
		return operation.Verification{}, err
	}
	passed := state.RuntimeValue == manifest.RuntimeValue && state.PersistedValue == "0" && state.PersistedDigest == manifest.PersistedDigest
	return operation.Verification{Passed: passed, Summary: verificationSummary(passed, "runtime sysctl restored", "runtime restore verification failed")}, nil
}

func decodeRestoreManifest(artifact operation.Artifact) (restoreManifest, error) {
	if artifact.SchemaVersion != restoreSchema {
		return restoreManifest{}, fmt.Errorf("unsupported sysctl restore schema %q", artifact.SchemaVersion)
	}
	var manifest restoreManifest
	if err := json.Unmarshal(artifact.Payload, &manifest); err != nil {
		return restoreManifest{}, err
	}
	manifest.CheckID = strings.TrimSpace(manifest.CheckID)
	manifest.Key = strings.TrimSpace(manifest.Key)
	manifest.RuntimeValue = strings.TrimSpace(manifest.RuntimeValue)
	manifest.PersistedDigest = strings.TrimSpace(manifest.PersistedDigest)
	if allowedChecks[manifest.CheckID] != manifest.Key || (manifest.RuntimeValue != "0" && manifest.RuntimeValue != "1") || manifest.PersistedDigest == "" {
		return restoreManifest{}, errors.New("sysctl restore manifest is invalid")
	}
	return manifest, nil
}

func verificationSummary(passed bool, success, failure string) string {
	if passed {
		return success
	}
	return failure
}
