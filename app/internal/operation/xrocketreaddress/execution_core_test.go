package xrocketreaddress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

type stageNoopExecutor struct{}

func (*stageNoopExecutor) Execute(context.Context, executor.Command) (executor.Result, error) {
	return executor.Result{}, errors.New("stage core must not execute free-form commands")
}

type stageLease struct {
	lease operation.LockLease
	err   error
}

func (value *stageLease) Current() operation.LockLease { return value.lease }
func (value *stageLease) Validate(time.Time) error     { return value.err }

type stageFakeAdapter struct {
	satisfied        map[string]bool
	mutations        []string
	inspections      []string
	leaveUnsatisfied bool
	aliasContracts   []AliasStageContract
	productContracts []ProductStageContract
	dbContracts      []ExternalDBStageContract
	etcdContracts    []EtcdStageContract
	confdContracts   []ConfdStageContract
	osContracts      []OSStageContract
	finalContracts   []FinalSiteStageContract
}

func newStageFakeAdapter() *stageFakeAdapter {
	return &stageFakeAdapter{satisfied: map[string]bool{}}
}

func stageStateKey(kind stageKind, nodeID string) string { return string(kind) + ":" + nodeID }

func (fake *stageFakeAdapter) inspect(kind stageKind, nodeID string) bool {
	fake.inspections = append(fake.inspections, stageStateKey(kind, nodeID))
	return fake.satisfied[stageStateKey(kind, nodeID)]
}

func (fake *stageFakeAdapter) mutate(kind stageKind, nodeID string) LocalMutationReceipt {
	key := stageStateKey(kind, nodeID)
	fake.mutations = append(fake.mutations, key)
	if !fake.leaveUnsatisfied {
		fake.satisfied[key] = true
	}
	return LocalMutationReceipt{Digest: digestBytes([]byte("fake-adapter:" + key)), State: MutationChanged}
}

func (fake *stageFakeAdapter) AddTargetAlias(_ context.Context, contract AliasStageContract) (LocalMutationReceipt, error) {
	fake.aliasContracts = append(fake.aliasContracts, contract)
	return fake.mutate(stageKindAlias, contract.NodeID), nil
}
func (fake *stageFakeAdapter) ReaddressProduct(_ context.Context, contract ProductStageContract) (LocalMutationReceipt, error) {
	fake.productContracts = append(fake.productContracts, contract)
	return fake.mutate(stageKindProduct, contract.NodeID), nil
}
func (fake *stageFakeAdapter) UpdateExternalDB(_ context.Context, contract ExternalDBStageContract) (LocalMutationReceipt, error) {
	fake.dbContracts = append(fake.dbContracts, contract)
	return fake.mutate(stageKindExternalDB, contract.NodeID), nil
}
func (fake *stageFakeAdapter) ReaddressEtcd(_ context.Context, contract EtcdStageContract) (LocalMutationReceipt, error) {
	fake.etcdContracts = append(fake.etcdContracts, contract)
	return fake.mutate(stageKindEtcd, contract.NodeID), nil
}
func (fake *stageFakeAdapter) RenderConfd(_ context.Context, contract ConfdStageContract) (LocalMutationReceipt, error) {
	fake.confdContracts = append(fake.confdContracts, contract)
	return fake.mutate(stageKindConfd, contract.NodeID), nil
}
func (fake *stageFakeAdapter) CutoverOS(_ context.Context, contract OSStageContract) (LocalMutationReceipt, error) {
	fake.osContracts = append(fake.osContracts, contract)
	return fake.mutate(stageKindOS, contract.NodeID), nil
}

func (fake *stageFakeAdapter) AliasSatisfied(_ context.Context, contract AliasStageContract) (bool, error) {
	return fake.inspect(stageKindAlias, contract.NodeID), nil
}
func (fake *stageFakeAdapter) ProductSatisfied(_ context.Context, contract ProductStageContract) (bool, error) {
	return fake.inspect(stageKindProduct, contract.NodeID), nil
}
func (fake *stageFakeAdapter) ExternalDBSatisfied(_ context.Context, contract ExternalDBStageContract) (bool, error) {
	return fake.inspect(stageKindExternalDB, contract.NodeID), nil
}
func (fake *stageFakeAdapter) EtcdSatisfied(_ context.Context, contract EtcdStageContract) (bool, error) {
	return fake.inspect(stageKindEtcd, contract.NodeID), nil
}
func (fake *stageFakeAdapter) ConfdSatisfied(_ context.Context, contract ConfdStageContract) (bool, error) {
	return fake.inspect(stageKindConfd, contract.NodeID), nil
}
func (fake *stageFakeAdapter) OSSatisfied(_ context.Context, contract OSStageContract) (bool, error) {
	return fake.inspect(stageKindOS, contract.NodeID), nil
}
func (fake *stageFakeAdapter) FinalSiteSatisfied(_ context.Context, contract FinalSiteStageContract) (bool, error) {
	fake.finalContracts = append(fake.finalContracts, contract)
	return fake.inspect(stageKindFinal, contract.NodeID), nil
}

func stageDefinition(t *testing.T, fake *stageFakeAdapter) *Definition {
	t.Helper()
	definition, err := NewDefinitionWithStageAdapters(&stageNoopExecutor{}, fake, fake)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func stageRuntime(t *testing.T, plan operation.Plan, stage operation.PlanStep) operation.RuntimeInput {
	t.Helper()
	decoded, err := decodeExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := json.Marshal(decoded.Parameters)
	if err != nil {
		t.Fatal(err)
	}
	return operation.RuntimeInput{Parameters: parameters, System: "linux", Targets: []operation.Target{stage.Target}}
}

func stageRestorePoint(t *testing.T, plan operation.Plan, stageIndex int) operation.RestorePoint {
	t.Helper()
	decoded, err := decodeExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	stage := plan.Steps[stageIndex]
	spec := canonicalStages[stageIndex]
	localAddress := decoded.Discovery.MasterAddress
	peerAddress := decoded.Discovery.SlaveAddress
	priority := 100
	runtimeRole := "active_vip_owner"
	memberID := "1234"
	bootID := "boot-master"
	interfaceAddresses := []restoreInterfaceAddress{{Address: decoded.Discovery.MasterAddress, PrefixLength: decoded.Discovery.PrefixLength}, {Address: decoded.Discovery.VIPAddress, PrefixLength: decoded.Discovery.PrefixLength}}
	if spec.Role == "slave" {
		localAddress = decoded.Discovery.SlaveAddress
		peerAddress = decoded.Discovery.MasterAddress
		priority = 90
		runtimeRole = "standby"
		memberID = "1235"
		bootID = "boot-slave"
		interfaceAddresses = []restoreInterfaceAddress{{Address: decoded.Discovery.SlaveAddress, PrefixLength: decoded.Discovery.PrefixLength}}
	}
	profile := decoded.MasterProfile
	before := restoreBeforeState{
		Site: restoreSiteState{MasterAddress: decoded.Discovery.MasterAddress, SlaveAddress: decoded.Discovery.SlaveAddress, VIPAddress: decoded.Discovery.VIPAddress},
		Network: restoreNetworkState{
			Address: localAddress, PrefixLength: decoded.Discovery.PrefixLength, Interface: decoded.Discovery.Interface,
			InterfaceAddresses: interfaceAddresses, Gateway: decoded.Discovery.GatewayAddress,
			Backend: profile.NetworkBackend, ConfigPath: profile.NetworkConfigPath,
			PersistentAddress: localAddress, PersistentPrefix: decoded.Discovery.PrefixLength, PersistentGateway: decoded.Discovery.GatewayAddress,
		},
		Product: restoreProductState{
			Generation: decoded.Discovery.ProductGeneration, VersionEvidence: decoded.Discovery.ProductVersionEvidence,
			InstallProfile: profile.InstallProfile, OSAuthorityUID: profile.OSAuthorityUID, ProductUser: profile.ProductUser,
			ProductHome: profile.ProductHome, ProductPrefix: profile.ProductPrefix, XrocketBinary: profile.XrocketBinary,
			XrocketVersion: profile.XrocketVersion, CommonYAMLPath: profile.CommonYAMLPath,
		},
		Database: restoreDatabaseState{Address: profile.ExternalDBAddress, Port: profile.ExternalDBPort},
		Etcd: restoreEtcdState{
			ConfigPath: profile.EtcdConfigPath, EtcdctlPath: profile.EtcdctlPath, Scheme: profile.EtcdScheme,
			ClientAddress: localAddress, ClientPort: profile.EtcdClientPort, PeerAddress: localAddress, PeerPort: profile.EtcdPeerPort,
			MemberID: memberID, MemberCount: 1, ServiceName: profile.EtcdServiceName, ControlAdapter: profile.ServiceControlAdapter,
		},
		HA: restoreHAState{
			ConfigPath: decoded.Discovery.KeepalivedConfigPath, ConfiguredRole: "BACKUP", RuntimeRole: runtimeRole,
			Interface: decoded.Discovery.Interface, SourceAddress: localAddress, PeerAddress: peerAddress, VIPAddress: decoded.Discovery.VIPAddress,
			Priority: priority, Nopreempt: true, BusinessPorts: append([]int(nil), decoded.Discovery.BusinessPorts...),
		},
		Rendering: restoreRenderingState{BootID: bootID, ConfdDestinations: append([]string(nil), profile.ConfdDestinations...)},
	}
	manifest := restorePointManifest{
		SchemaVersion: RestorePointSchema, OperationID: OperationID, OperationVersion: Metadata().Version,
		RunID: "run-apply-core-001", StageID: stage.ID, StageIndex: stageIndex, NodeID: stage.ExecutorNodeID,
		ParticipantNodeIDs: []string{"node-master", "node-slave"}, Role: spec.Role, Before: before,
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	point := operation.RestorePoint{
		ID: restorePointID(manifest.RunID, manifest.NodeID, manifest.StageID), ProviderID: RestorePointProviderID,
		OperationID: OperationID, RunID: manifest.RunID, Status: operation.RestorePointVerified,
		Targets: []operation.Target{stage.Target}, CreatedAt: now.Add(-time.Minute), ExpiresAt: &expires,
		Manifest: operation.Artifact{SchemaVersion: RestorePointSchema, Payload: payload},
	}
	if _, err := decodeRestoreManifest(point); err != nil {
		t.Fatalf("fixture restore manifest invalid: %v", err)
	}
	return point
}

func stageApplyInput(t *testing.T, plan operation.Plan, stageIndex int) operation.ApplyInput {
	t.Helper()
	stage := plan.Steps[stageIndex]
	decoded, err := decodeExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	point := stageRestorePoint(t, plan, stageIndex)
	input := operation.ApplyInput{
		Runtime: stageRuntime(t, plan, stage), Plan: plan, Stage: &stage, Impact: buildImpact(decoded), RestorePoint: point,
	}
	if stage.Writes {
		resourceKey, err := operation.ResourceLockKey(stage.Target)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		input.Lease = &stageLease{lease: operation.LockLease{
			ID: "lease-stage", OwnerID: point.RunID, Resources: []operation.LockResource{{Key: resourceKey}},
			AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		}}
	}
	return input
}

func rewriteRestoreManifest(t *testing.T, point operation.RestorePoint, change func(*restorePointManifest)) operation.RestorePoint {
	t.Helper()
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	change(&manifest)
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	point.Manifest.Payload = payload
	return point
}

func TestApplyVerifyStageContractCoversAll15FrozenStages(t *testing.T) {
	plan := restorePlan(t)
	if len(plan.Steps) != 15 || len(canonicalStages) != 15 {
		t.Fatalf("plan stages=%d canonical=%d", len(plan.Steps), len(canonicalStages))
	}

	for stageIndex := range plan.Steps {
		stageIndex := stageIndex
		stage := plan.Steps[stageIndex]
		spec := canonicalStages[stageIndex]
		t.Run(fmt.Sprintf("%02d-%s", stageIndex, stage.ID), func(t *testing.T) {
			fake := newStageFakeAdapter()
			definition := stageDefinition(t, fake)
			input := stageApplyInput(t, plan, stageIndex)
			if spec.Kind == stageKindFinal {
				fake.satisfied[stageStateKey(stageKindFinal, stage.ExecutorNodeID)] = true
			}

			applied, err := definition.Apply(context.Background(), input)
			if err != nil {
				t.Fatalf("Apply err=%v", err)
			}
			receipt, err := decodeApplyStageReceipt(applied)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.StageID != stage.ID || receipt.StageIndex != stageIndex || receipt.NodeID != stage.ExecutorNodeID || receipt.Role != spec.Role || receipt.Expectation.Kind != spec.Kind {
				t.Fatalf("receipt=%#v", receipt)
			}
			if stage.Target.NodeID != stage.ExecutorNodeID || receipt.NodeID != stage.Target.NodeID {
				t.Fatalf("local participant mismatch stage=%#v receipt=%#v", stage, receipt)
			}
			if spec.Kind == stageKindOS && (receipt.Barrier != operation.StageBarrierAgentReconnect || receipt.Expectation.OS == nil || receipt.Expectation.OS.Barrier != operation.StageBarrierAgentReconnect || !receipt.Expectation.OS.Reboot) {
				t.Fatalf("OS reconnect contract=%#v", receipt)
			}
			if spec.Writes {
				wantMutation := stageStateKey(spec.Kind, stage.ExecutorNodeID)
				if !applied.Changed || !reflect.DeepEqual(fake.mutations, []string{wantMutation}) {
					t.Fatalf("changed=%v mutations=%v want=%s", applied.Changed, fake.mutations, wantMutation)
				}
			} else if applied.Changed || len(fake.mutations) != 0 {
				t.Fatalf("non-writing stage mutated: result=%#v calls=%v", applied, fake.mutations)
			}

			verified, err := definition.Verify(context.Background(), operation.VerifyInput{Runtime: input.Runtime, Plan: plan, Stage: input.Stage, Apply: applied})
			if err != nil || !verified.Passed {
				t.Fatalf("Verify=%#v err=%v", verified, err)
			}

			mutationCount := len(fake.mutations)
			retry, err := definition.Apply(context.Background(), input)
			if err != nil {
				t.Fatalf("retry Apply err=%v", err)
			}
			if len(fake.mutations) != mutationCount {
				t.Fatalf("exact retry performed second mutation: before=%d after=%d", mutationCount, len(fake.mutations))
			}
			if spec.Writes && retry.Changed {
				t.Fatalf("exact retry did not converge to deterministic no-op: %#v", retry)
			}
			secondRetry, err := definition.Apply(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(retry.State.Payload, secondRetry.State.Payload) || retry.Changed != secondRetry.Changed {
				t.Fatalf("stable retry receipts differ: first=%s second=%s", retry.State.Payload, secondRetry.State.Payload)
			}
		})
	}
}

func TestApplyFailsClosedBeforeMutationOnCorrelationMismatch(t *testing.T) {
	const stageIndex = 2 // alias-slave is the first mutating stage.
	tests := []struct {
		name   string
		mutate func(*operation.ApplyInput)
	}{
		{name: "plan-schema", mutate: func(input *operation.ApplyInput) { input.Plan.SchemaVersion = "wrong.plan.v1" }},
		{name: "stage-action", mutate: func(input *operation.ApplyInput) {
			input.Plan.Steps[stageIndex].Action = "wrong_action"
			stage := input.Plan.Steps[stageIndex]
			input.Stage = &stage
		}},
		{name: "stage-checkpoint", mutate: func(input *operation.ApplyInput) {
			input.Plan.Steps[stageIndex].Checkpoint = "wrong_checkpoint"
			stage := input.Plan.Steps[stageIndex]
			input.Stage = &stage
		}},
		{name: "stage-writes", mutate: func(input *operation.ApplyInput) {
			input.Plan.Steps[stageIndex].Writes = false
			stage := input.Plan.Steps[stageIndex]
			input.Stage = &stage
		}},
		{name: "wrong-local-node", mutate: func(input *operation.ApplyInput) {
			input.Runtime.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-master"}}
		}},
		{name: "runtime-parameters", mutate: func(input *operation.ApplyInput) {
			input.Runtime.Parameters = []byte(`{"master_target_address":"198.51.100.20","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","external_db_target_address":"203.0.113.20"}`)
		}},
		{name: "restore-role", mutate: func(input *operation.ApplyInput) {
			input.RestorePoint = rewriteRestoreManifest(t, input.RestorePoint, func(value *restorePointManifest) { value.Role = "master" })
		}},
		{name: "restore-participants", mutate: func(input *operation.ApplyInput) {
			input.RestorePoint = rewriteRestoreManifest(t, input.RestorePoint, func(value *restorePointManifest) { value.ParticipantNodeIDs = []string{"node-other", "node-slave"} })
		}},
		{name: "new-ip-substitutes-old-state", mutate: func(input *operation.ApplyInput) {
			input.RestorePoint = rewriteRestoreManifest(t, input.RestorePoint, func(value *restorePointManifest) { value.Before.Network.Address = "198.51.100.11" })
		}},
		{name: "restore-version", mutate: func(input *operation.ApplyInput) {
			input.RestorePoint = rewriteRestoreManifest(t, input.RestorePoint, func(value *restorePointManifest) { value.OperationVersion = "9.9.9" })
		}},
		{name: "wrong-lease-owner", mutate: func(input *operation.ApplyInput) {
			lease := input.Lease.(*stageLease)
			lease.lease.OwnerID = "other-run"
		}},
		{name: "wrong-lease-target", mutate: func(input *operation.ApplyInput) {
			lease := input.Lease.(*stageLease)
			lease.lease.Resources = []operation.LockResource{{Key: "node|other"}}
		}},
		{name: "expired-restore", mutate: func(input *operation.ApplyInput) {
			expired := time.Now().Add(-time.Second)
			input.RestorePoint.ExpiresAt = &expired
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newStageFakeAdapter()
			definition := stageDefinition(t, fake)
			input := stageApplyInput(t, restorePlan(t), stageIndex)
			testCase.mutate(&input)
			if _, err := definition.Apply(context.Background(), input); err == nil {
				t.Fatal("invalid correlation was accepted")
			}
			if len(fake.mutations) != 0 {
				t.Fatalf("mutation occurred before correlation failure: %v", fake.mutations)
			}
		})
	}
}

func TestExternalDBEtcdConfdAndOSContractsRemainBoundedAndSecretFree(t *testing.T) {
	plan := restorePlan(t)
	indexes := []int{6, 8, 10, 12}
	for _, stageIndex := range indexes {
		fake := newStageFakeAdapter()
		definition := stageDefinition(t, fake)
		input := stageApplyInput(t, plan, stageIndex)
		applied, err := definition.Apply(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := decodeApplyStageReceipt(applied)
		if err != nil {
			t.Fatal(err)
		}
		switch canonicalStages[stageIndex].Kind {
		case stageKindExternalDB:
			contract := receipt.Expectation.ExternalDB
			if contract == nil || contract.OldAddress != "203.0.113.80" || contract.NewAddress != "203.0.113.20" || contract.Port != 8888 || !contract.PreserveNonAddressFields {
				t.Fatalf("external DB contract=%#v", contract)
			}
		case stageKindEtcd:
			contract := receipt.Expectation.Etcd
			if contract == nil || contract.MemberCount != 1 || contract.MemberID == "" || contract.OldClient != "192.0.2.11" || contract.NewClient != "198.51.100.11" || contract.OldPeer != "192.0.2.11" || contract.NewPeer != "198.51.100.11" {
				t.Fatalf("etcd contract=%#v", contract)
			}
		case stageKindConfd:
			contract := receipt.Expectation.Confd
			if contract == nil || !reflect.DeepEqual(contract.Destinations, []string{"/etc/xrocket/rendered.conf"}) {
				t.Fatalf("confd contract=%#v", contract)
			}
		case stageKindOS:
			contract := receipt.Expectation.OS
			if contract == nil || contract.Barrier != operation.StageBarrierAgentReconnect || !contract.Reboot {
				t.Fatalf("OS contract=%#v", contract)
			}
		}
		lower := strings.ToLower(string(applied.State.Payload))
		for _, forbidden := range []string{restoreFixtureSecret, "password", "private_key", "authorization", "token", "raw_output", "stdout", "stderr"} {
			if strings.Contains(lower, strings.ToLower(forbidden)) {
				t.Fatalf("ApplyResult leaked forbidden material %q: %s", forbidden, applied.State.Payload)
			}
		}
	}
}

func TestVerifyCatchesAdapterSuccessWithWrongObservedPostcondition(t *testing.T) {
	const stageIndex = 4 // product-slave
	plan := restorePlan(t)
	fake := newStageFakeAdapter()
	fake.leaveUnsatisfied = true
	definition := stageDefinition(t, fake)
	input := stageApplyInput(t, plan, stageIndex)
	applied, err := definition.Apply(context.Background(), input)
	if err != nil || !applied.Changed || len(fake.mutations) != 1 {
		t.Fatalf("Apply=%#v mutations=%v err=%v", applied, fake.mutations, err)
	}
	verification, err := definition.Verify(context.Background(), operation.VerifyInput{Runtime: input.Runtime, Plan: plan, Stage: input.Stage, Apply: applied})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Passed || len(verification.Findings) != 1 || verification.Findings[0].Code != "XROCKET_STAGE_POSTCONDITION_MISMATCH" {
		t.Fatalf("Verify=%#v", verification)
	}
}

func TestProductionDefinitionAndRollbackRemainFailClosed(t *testing.T) {
	production, err := NewDefinition(&stageNoopExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := production.Apply(context.Background(), operation.ApplyInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("production Apply err=%v", err)
	}
	if _, err := production.Verify(context.Background(), operation.VerifyInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("production Verify err=%v", err)
	}
	fake := newStageFakeAdapter()
	injected := stageDefinition(t, fake)
	if _, err := injected.Rollback(context.Background(), operation.RollbackInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("Rollback err=%v", err)
	}
	if _, err := injected.VerifyRollback(context.Background(), operation.VerifyRollbackInput{}); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("VerifyRollback err=%v", err)
	}
}

type failingStageAdapter struct{ *stageFakeAdapter }

func (fake *failingStageAdapter) AddTargetAlias(_ context.Context, contract AliasStageContract) (LocalMutationReceipt, error) {
	fake.aliasContracts = append(fake.aliasContracts, contract)
	fake.mutations = append(fake.mutations, stageStateKey(stageKindAlias, contract.NodeID))
	return LocalMutationReceipt{
		Digest: digestBytes([]byte("fake-partial-alias")),
		State:  MutationMayHaveChanged,
	}, errors.New("injected ambiguous alias mutation failure")
}

func TestApplyRetainsTypedAmbiguousMutationEvidenceOnError(t *testing.T) {
	const stageIndex = 2
	plan := restorePlan(t)
	fake := &failingStageAdapter{stageFakeAdapter: newStageFakeAdapter()}
	definition, err := NewDefinitionWithStageAdapters(&stageNoopExecutor{}, fake, fake)
	if err != nil {
		t.Fatal(err)
	}
	input := stageApplyInput(t, plan, stageIndex)
	result, applyErr := definition.Apply(context.Background(), input)
	if applyErr == nil || !result.Changed || result.Checkpoint == "" || len(result.State.Payload) == 0 {
		t.Fatalf("result=%#v err=%v", result, applyErr)
	}
	receipt, err := decodeApplyStageReceipt(result)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Failed || receipt.AdapterReceipt == nil || receipt.AdapterReceipt.State != MutationMayHaveChanged {
		t.Fatalf("typed failure receipt=%#v", receipt)
	}
	if result.Reconnect != nil {
		t.Fatalf("failed mutation must not auto-request reboot: %#v", result.Reconnect)
	}
}
