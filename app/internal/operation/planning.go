package operation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PlanningFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Block struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	SafeNext     string `json:"safe_next_action"`
	ManualReview bool   `json:"manual_review"`
}

type PlanningResult struct {
	OperationID      string           `json:"operation_id"`
	OperationVersion string           `json:"operation_version"`
	CapabilityDigest string           `json:"capability_digest"`
	State            State            `json:"state"`
	Checkpoint       string           `json:"checkpoint"`
	StartedAt        time.Time        `json:"started_at"`
	CompletedAt      time.Time        `json:"completed_at"`
	Discovery        *Discovery       `json:"discovery,omitempty"`
	Precheck         *Precheck        `json:"precheck,omitempty"`
	Plan             *Plan            `json:"plan,omitempty"`
	Impact           *Impact          `json:"impact,omitempty"`
	PlanDigest       string           `json:"plan_digest,omitempty"`
	Block            *Block           `json:"block,omitempty"`
	Error            *PlanningFailure `json:"error,omitempty"`
}

func ExecutePlanning(
	ctx context.Context,
	definition PlanningDefinition,
	runtime RuntimeInput,
	now func() time.Time,
) PlanningResult {
	started := now().UTC()
	metadata := definition.Metadata()
	digest, digestErr := CapabilityDigest(metadata)
	result := PlanningResult{
		OperationID: metadata.ID, OperationVersion: metadata.Version, CapabilityDigest: digest,
		State: StateDiscovering, Checkpoint: "discovering", StartedAt: started,
	}
	finishFailure := func(code string, err error) PlanningResult {
		result.CompletedAt = now().UTC()
		result.Checkpoint = code
		result.Error = &PlanningFailure{Code: code, Message: err.Error()}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.State = StateInterrupted
		} else {
			result.State = StateBlocked
			result.Block = &Block{Code: code, Message: err.Error(), SafeNext: "inspect_the_failure_and_create_a_new_run", ManualReview: true}
		}
		return result
	}
	if digestErr != nil {
		return finishFailure("operation_contract_invalid", digestErr)
	}
	discovery, err := definition.Discover(ctx, DiscoverInput{Runtime: runtime})
	if err != nil {
		return finishFailure("operation_discovery_failed", err)
	}
	result.Discovery = &discovery
	if !discovery.Applicable {
		result.State = StateBlocked
		result.Checkpoint = "discovery_blocked"
		result.Block = &Block{Code: "operation_not_applicable", Message: discovery.Summary, SafeNext: "select_an_applicable_target", ManualReview: true}
		result.CompletedAt = now().UTC()
		return result
	}
	if len(discovery.Targets) == 0 {
		return finishFailure("operation_discovery_empty", errors.New("applicable operation discovery returned no physical targets"))
	}
	result.State = StatePrechecking
	result.Checkpoint = "prechecking"
	precheck, err := definition.Precheck(ctx, PrecheckInput{Runtime: runtime, Discovery: discovery})
	if err != nil {
		return finishFailure("operation_precheck_failed", err)
	}
	result.Precheck = &precheck
	if !precheck.Passed {
		result.State = StateBlocked
		result.Checkpoint = "precheck_blocked"
		result.Block = &Block{Code: "operation_precheck_blocked", Message: precheck.Summary, SafeNext: "resolve_blocking_findings_and_create_a_new_run", ManualReview: true}
		result.CompletedAt = now().UTC()
		return result
	}
	plan, err := definition.Plan(ctx, PlanInput{Runtime: runtime, Discovery: discovery, Precheck: precheck})
	if err != nil {
		return finishFailure("operation_plan_failed", err)
	}
	impact, err := definition.Impact(ctx, ImpactInput{Runtime: runtime, Plan: plan})
	if err != nil {
		return finishFailure("operation_impact_failed", err)
	}
	planDigest, err := PlanDigest(digest, runtime.Targets, runtime.Parameters, runtime.SecretRefs, plan, impact)
	if err != nil {
		return finishFailure("operation_plan_digest_failed", err)
	}
	result.Plan = &plan
	result.State = StatePlanned
	result.Checkpoint = "planned"
	result.Impact = &impact
	result.PlanDigest = planDigest
	result.State = StateAwaitingConfirm
	result.Checkpoint = "plan_ready"
	result.CompletedAt = now().UTC()
	return result
}

func PlanDigest(
	capabilityDigest string,
	targets []Target,
	parameters json.RawMessage,
	secretRefs []SecretRef,
	plan Plan,
	impact Impact,
) (string, error) {
	if capabilityDigest == "" {
		return "", errors.New("capability digest is required")
	}
	if len(targets) == 0 {
		return "", errors.New("frozen operation targets are required")
	}
	if len(parameters) == 0 || !json.Valid(parameters) {
		return "", errors.New("frozen operation parameters must be valid JSON")
	}
	// Optional empty collections may be omitted at the HTTP boundary. Keep
	// their digest representation stable across nil and empty slices.
	canonicalSecretRefs := make([]SecretRef, 0, len(secretRefs))
	canonicalSecretRefs = append(canonicalSecretRefs, secretRefs...)
	encoded, err := json.Marshal(struct {
		CapabilityDigest string          `json:"capability_digest"`
		Targets          []Target        `json:"targets"`
		Parameters       json.RawMessage `json:"parameters"`
		SecretRefs       []SecretRef     `json:"secret_refs"`
		Plan             Plan            `json:"plan"`
		Impact           Impact          `json:"impact"`
	}{CapabilityDigest: capabilityDigest, Targets: targets, Parameters: parameters, SecretRefs: canonicalSecretRefs, Plan: plan, Impact: impact})
	if err != nil {
		return "", fmt.Errorf("encode operation plan digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return digestPrefix + hex.EncodeToString(digest[:]), nil
}
