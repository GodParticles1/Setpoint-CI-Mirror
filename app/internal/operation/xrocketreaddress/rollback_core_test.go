package xrocketreaddress

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"setpoint/internal/operation"
)

type rollbackFixtureAdapter struct {
	*stageFakeAdapter
	artifactHashes       map[string]string
	restoreCalls         []rollbackStageExpectation
	forcePostcondition   bool
	etcdObservation      EtcdRecoveryObservation
	rollbackBootBefore   string
	rollbackBootAfter    string
	rollbackMutationHash string
}

func newRollbackFixtureAdapter() *rollbackFixtureAdapter {
	return &rollbackFixtureAdapter{
		stageFakeAdapter:     newStageFakeAdapter(),
		artifactHashes:       map[string]string{},
		rollbackBootBefore:   "boot-after-apply",
		rollbackBootAfter:    "boot-after-rollback",
		rollbackMutationHash: digestBytes([]byte("rollback-mutation")),
	}
}

func (fake *rollbackFixtureAdapter) RestoreStage(_ context.Context, expectation rollbackStageExpectation) (RollbackMutationReceipt, error) {
	fake.restoreCalls = append(fake.restoreCalls, expectation)
	receipt := RollbackMutationReceipt{Digest: fake.rollbackMutationHash}
	if expectation.Kind == stageKindOS {
		receipt.BootIDBeforeRollback = fake.rollbackBootBefore
		receipt.BootIDAfterRollback = fake.rollbackBootAfter
	}
	return receipt, nil
}

func (fake *rollbackFixtureAdapter) RecoveryArtifactMatches(_ context.Context, artifact RecoveryArtifactRef) (bool, error) {
	return fake.artifactHashes[artifact.BackupRef] == artifact.SHA256, nil
}

func (fake *rollbackFixtureAdapter) EtcdRecoveryState(_ context.Context, contract RollbackEtcdContract) (EtcdRecoveryObservation, error) {
	value := fake.etcdObservation
	if value.MemberID == "" {
		value.MemberID = contract.MemberID
		value.MemberCount = contract.MemberCount
		value.Healthy = true
		value.LogicalKVDigest = contract.LogicalKVDigest
	}
	return value, nil
}

func (fake *rollbackFixtureAdapter) CurrentBootID(_ context.Context, _ RollbackOSContract) (string, error) {
	return fake.rollbackBootBefore, nil
}

func (fake *rollbackFixtureAdapter) InspectRollback(_ context.Context, expectation rollbackStageExpectation) (RollbackObservation, error) {
	if fake.forcePostcondition {
		return RollbackObservation{Satisfied: false}, nil
	}
	observation := RollbackObservation{Satisfied: true}
	if expectation.Etcd != nil {
		contract := expectation.Etcd
		observation.Etcd = &EtcdRecoveryObservation{
			MemberID: contract.MemberID, MemberCount: contract.MemberCount,
			ClientAddress: contract.OldClient, PeerAddress: contract.OldPeer,
			Healthy: true, LogicalKVDigest: contract.LogicalKVDigest,
		}
	}
	if expectation.OS != nil {
		observation.BootID = fake.rollbackBootAfter
	}
	return observation, nil
}

type fixtureRecoveryCollector struct{}

func (fixtureRecoveryCollector) Capture(_ context.Context, request RecoveryArtifactCaptureRequest) (RecoveryArtifactContract, error) {
	return fixtureRecoveryContract(request.Owner, request.StageKind, request.Before), nil
}

func fixtureRecoveryContract(owner RecoveryArtifactOwner, stageKindValue string, before restoreBeforeState) RecoveryArtifactContract {
	contract := RecoveryArtifactContract{SchemaVersion: RecoveryArtifactContractSchema, Owner: owner}
	add := func(kind, source, name string) {
		ref := RecoveryArtifactRef{
			OwnerID: owner.OwnerID,
			Kind:    kind, SourcePath: source,
			BackupRef: recoveryBackupRoot(owner.OwnerID) + "/" + name,
			SHA256:    digestBytes([]byte("fixture:" + kind + ":" + source)),
		}
		ref.ID = recoveryArtifactID(ref)
		contract.Artifacts = append(contract.Artifacts, ref)
	}
	switch stageKind(stageKindValue) {
	case stageKindAlias:
		add(recoveryKindNetworkConfig, before.Network.ConfigPath, "network-config")
	case stageKindProduct:
		add(recoveryKindProductBundle, before.Product.VersionEvidence, "product-config-bundle")
		add(recoveryKindKeepalivedConfig, before.HA.ConfigPath, "keepalived-config")
	case stageKindExternalDB:
		add(recoveryKindCommonYAML, before.Product.CommonYAMLPath, "common-yaml")
	case stageKindEtcd:
		add(recoveryKindEtcdConfig, before.Etcd.ConfigPath, "etcd-config")
		add(recoveryKindEtcdSnapshot, before.Etcd.EtcdctlPath, "etcd-snapshot.db")
		contract.LogicalKVDigest = digestBytes([]byte("logical-kv-baseline"))
	case stageKindConfd:
		for index, destination := range before.Rendering.ConfdDestinations {
			add(recoveryKindConfdDestination, destination, "confd-destination-"+string(rune('a'+index)))
		}
	case stageKindOS:
		add(recoveryKindNetworkConfig, before.Network.ConfigPath, "network-config")
	}
	sort.Slice(contract.Artifacts, func(left, right int) bool {
		if contract.Artifacts[left].Kind != contract.Artifacts[right].Kind {
			return contract.Artifacts[left].Kind < contract.Artifacts[right].Kind
		}
		return contract.Artifacts[left].SourcePath < contract.Artifacts[right].SourcePath
	})
	return contract
}

func rollbackRestorePoint(t *testing.T, plan operation.Plan, stageIndex int) operation.RestorePoint {
	t.Helper()
	point := stageRestorePoint(t, plan, stageIndex)
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = RestorePointRollbackSchema
	owner := recoveryOwnerFromManifest(manifest)
	recovery := fixtureRecoveryContract(owner, string(canonicalStages[stageIndex].Kind), manifest.Before)
	manifest.Recovery = &recovery
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	point.Manifest = operation.Artifact{SchemaVersion: RestorePointRollbackSchema, Payload: payload}
	if _, err := decodeRestoreManifest(point); err != nil {
		t.Fatalf("rollback RestorePoint fixture invalid: %v", err)
	}
	return point
}

func rollbackDefinition(t *testing.T, fake *rollbackFixtureAdapter) *Definition {
	t.Helper()
	definition, err := NewDefinitionWithStageAdapters(&stageNoopExecutor{}, fake, fake)
	if err != nil {
		t.Fatal(err)
	}
	if definition.rollbackMutator == nil || definition.rollbackInspector == nil {
		t.Fatal("rollback adapters were not bound by the bounded stage constructor")
	}
	return definition
}

func rollbackInput(t *testing.T, fake *rollbackFixtureAdapter, stageIndex int) (*Definition, operation.RollbackInput) {
	t.Helper()
	plan := restorePlan(t)
	point := rollbackRestorePoint(t, plan, stageIndex)
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range manifest.Recovery.Artifacts {
		fake.artifactHashes[artifact.BackupRef] = artifact.SHA256
	}
	definition := rollbackDefinition(t, fake)
	applyInput := stageApplyInput(t, plan, stageIndex)
	applyInput.RestorePoint = point
	applied, err := definition.Apply(context.Background(), applyInput)
	if err != nil {
		t.Fatalf("Apply before rollback err=%v", err)
	}
	stage := plan.Steps[stageIndex]
	return definition, operation.RollbackInput{
		Runtime: applyInput.Runtime, Plan: plan, Stage: &stage, Apply: applied,
		RestorePoint: point, Lease: applyInput.Lease,
	}
}

func TestRollbackRestorePointV2BindsRunOwnedRecoveryArtifacts(t *testing.T) {
	commandExecutor := &restoreFixtureExecutor{localAddress: "192.0.2.10", peerAddress: "192.0.2.11", vipOwner: true, priority: 100, memberID: 4660, bootID: "boot-master"}
	fake := newRollbackFixtureAdapter()
	provider, err := newRestorePointProviderWithRecovery(commandExecutor, fixtureRecoveryCollector{}, fake)
	if err != nil {
		t.Fatal(err)
	}
	request := restoreRequest(t, "master")
	stage := request.Plan.Steps[9]
	request.Stage = &stage
	request.Targets = []operation.Target{stage.Target}
	point, err := provider.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != RestorePointRollbackSchema || manifest.Recovery == nil || manifest.Recovery.Owner.RunID != request.RunID || manifest.Recovery.Owner.NodeID != stage.ExecutorNodeID || manifest.Recovery.Owner.StageID != stage.ID || manifest.Recovery.Owner.StageIndex != 9 {
		t.Fatalf("recovery ownership=%#v", manifest.Recovery)
	}
	if len(manifest.Recovery.Artifacts) != 2 || !validSHA256Digest(manifest.Recovery.LogicalKVDigest) {
		t.Fatalf("etcd recovery contract=%#v", manifest.Recovery)
	}
	for _, artifact := range manifest.Recovery.Artifacts {
		if !strings.HasPrefix(artifact.BackupRef, recoveryBackupRoot(manifest.Recovery.Owner.OwnerID)+"/") || artifact.OwnerID != manifest.Recovery.Owner.OwnerID || !validSHA256Digest(artifact.SHA256) || !validSHA256Digest(artifact.ID) {
			t.Fatalf("unbound recovery artifact=%#v", artifact)
		}
	}
}

func TestRollbackCorrelationMismatchFailsBeforeMutation(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 4)
	input.Runtime.Parameters = []byte(`{"master_target_address":"198.51.100.99","slave_target_address":"198.51.100.11","vip_target_address":"198.51.100.12","external_db_target_address":"203.0.113.20"}`)
	if _, err := definition.Rollback(context.Background(), input); err == nil {
		t.Fatal("rollback accepted runtime/plan correlation mismatch")
	}
	if len(fake.restoreCalls) != 0 {
		t.Fatalf("rollback mutation occurred before correlation failure: %#v", fake.restoreCalls)
	}
}

func TestRollbackRecoveryArtifactHashFailsClosed(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 4)
	manifest, err := decodeRestoreManifest(input.RestorePoint)
	if err != nil {
		t.Fatal(err)
	}
	artifact := manifest.Recovery.Artifacts[0]
	fake.artifactHashes[artifact.BackupRef] = digestBytes([]byte("wrong-local-artifact"))
	if _, err := definition.Rollback(context.Background(), input); err == nil || !strings.Contains(err.Error(), "hash/identity") {
		t.Fatalf("wrong recovery artifact hash err=%v", err)
	}
	if len(fake.restoreCalls) != 0 {
		t.Fatalf("rollback mutation occurred with wrong recovery artifact hash: %#v", fake.restoreCalls)
	}
}

func TestRollbackEvidenceNeverPersistsSecrets(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 6)
	result, err := definition.Rollback(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	combined := strings.ToLower(string(input.RestorePoint.Manifest.Payload) + string(result.State.Payload))
	for _, forbidden := range []string{restoreFixtureSecret, "password", "private_key", "authorization", "token", "raw_output", "stdout", "stderr"} {
		if strings.Contains(combined, strings.ToLower(forbidden)) {
			t.Fatalf("rollback evidence persisted forbidden material %q: %s", forbidden, combined)
		}
	}
}

func TestRollbackNoopStagePerformsZeroMutation(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	plan := restorePlan(t)
	stageIndex := 2
	stage := plan.Steps[stageIndex]
	fake.satisfied[stageStateKey(canonicalStages[stageIndex].Kind, stage.ExecutorNodeID)] = true
	point := rollbackRestorePoint(t, plan, stageIndex)
	manifest, _ := decodeRestoreManifest(point)
	for _, artifact := range manifest.Recovery.Artifacts {
		fake.artifactHashes[artifact.BackupRef] = artifact.SHA256
	}
	definition := rollbackDefinition(t, fake)
	applyInput := stageApplyInput(t, plan, stageIndex)
	applyInput.RestorePoint = point
	applied, err := definition.Apply(context.Background(), applyInput)
	if err != nil || applied.Changed {
		t.Fatalf("expected no-op Apply, result=%#v err=%v", applied, err)
	}
	result, err := definition.Rollback(context.Background(), operation.RollbackInput{Runtime: applyInput.Runtime, Plan: plan, Stage: &stage, Apply: applied, RestorePoint: point, Lease: applyInput.Lease})
	if err != nil || !result.Restored {
		t.Fatalf("no-op rollback=%#v err=%v", result, err)
	}
	if len(fake.restoreCalls) != 0 {
		t.Fatalf("no-op Apply invented rollback mutation: %#v", fake.restoreCalls)
	}
}

func TestRollbackLeaseFailsClosedBeforeMutation(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		fake := newRollbackFixtureAdapter()
		definition, input := rollbackInput(t, fake, 2)
		input.Lease.(*stageLease).err = errors.New("stale lease")
		if _, err := definition.Rollback(context.Background(), input); err == nil {
			t.Fatal("stale rollback lease was accepted")
		}
		if len(fake.restoreCalls) != 0 {
			t.Fatalf("stale lease allowed mutation: %#v", fake.restoreCalls)
		}
	})
	t.Run("foreign", func(t *testing.T) {
		fake := newRollbackFixtureAdapter()
		definition, input := rollbackInput(t, fake, 2)
		input.Lease.(*stageLease).lease.OwnerID = "other-run"
		if _, err := definition.Rollback(context.Background(), input); err == nil {
			t.Fatal("foreign rollback lease was accepted")
		}
		if len(fake.restoreCalls) != 0 {
			t.Fatalf("foreign lease allowed mutation: %#v", fake.restoreCalls)
		}
	})
}

func TestRollbackEtcdDigestDriftFailsBeforeMutation(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 8)
	manifest, _ := decodeRestoreManifest(input.RestorePoint)
	fake.etcdObservation = EtcdRecoveryObservation{MemberID: manifest.Before.Etcd.MemberID, MemberCount: 1, Healthy: true, LogicalKVDigest: digestBytes([]byte("drifted-kv"))}
	if _, err := definition.Rollback(context.Background(), input); err == nil || !strings.Contains(err.Error(), "logical KV digest drifted") {
		t.Fatalf("etcd drift err=%v", err)
	}
	if len(fake.restoreCalls) != 0 {
		t.Fatalf("etcd drift allowed rollback mutation: %#v", fake.restoreCalls)
	}
}

func TestRollbackProductUsesRecoveryArtifactWithoutReverseUpdateIP(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 4)
	if _, err := definition.Rollback(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(fake.restoreCalls) != 1 || fake.restoreCalls[0].Product == nil {
		t.Fatalf("product rollback calls=%#v", fake.restoreCalls)
	}
	contract := fake.restoreCalls[0].Product
	if contract.OldMaster != "192.0.2.10" || contract.OldSlave != "192.0.2.11" || contract.OldVIP != "192.0.2.12" || contract.ProductBundle.Kind != recoveryKindProductBundle || contract.KeepalivedConfig.Kind != recoveryKindKeepalivedConfig {
		t.Fatalf("product rollback contract=%#v", contract)
	}
	payload, _ := json.Marshal(contract)
	if strings.Contains(string(payload), "198.51.100.") || strings.Contains(strings.ToLower(string(payload)), "updateip") {
		t.Fatalf("product rollback leaked target/reverse-update construction: %s", payload)
	}
}

func TestRollbackExternalDBAddressOnlyContract(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 6)
	if _, err := definition.Rollback(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(fake.restoreCalls) != 1 || fake.restoreCalls[0].ExternalDB == nil {
		t.Fatalf("external DB rollback calls=%#v", fake.restoreCalls)
	}
	contract := fake.restoreCalls[0].ExternalDB
	if contract.OldAddress != "203.0.113.80" || contract.Port != 8888 || !contract.AddressOnly || contract.CommonYAMLPath != "/etc/confd/common.yaml" || contract.BaselineConfig.SourcePath != contract.CommonYAMLPath {
		t.Fatalf("external DB rollback contract=%#v", contract)
	}
}

func TestRollbackOSReconnectBarrierAndBootTransition(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 12)
	result, err := definition.Rollback(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeRollbackStageReceipt(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.restoreCalls) != 1 || fake.restoreCalls[0].OS == nil || fake.restoreCalls[0].OS.Barrier != operation.StageBarrierAgentReconnect || !fake.restoreCalls[0].OS.Reboot {
		t.Fatalf("OS rollback contract=%#v", fake.restoreCalls)
	}
	if receipt.Barrier != operation.StageBarrierAgentReconnect || receipt.AdapterReceipt == nil || receipt.AdapterReceipt.BootIDBeforeRollback == receipt.AdapterReceipt.BootIDAfterRollback {
		t.Fatalf("OS rollback receipt=%#v", receipt)
	}
	verification, err := definition.VerifyRollback(context.Background(), operation.VerifyRollbackInput{Runtime: input.Runtime, Plan: input.Plan, Stage: input.Stage, Rollback: result, RestorePoint: input.RestorePoint})
	if err != nil || !verification.Passed {
		t.Fatalf("OS VerifyRollback=%#v err=%v", verification, err)
	}
}

func TestVerifyRollbackRejectsWrongObservedPostcondition(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 2)
	result, err := definition.Rollback(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	fake.forcePostcondition = true
	verification, err := definition.VerifyRollback(context.Background(), operation.VerifyRollbackInput{Runtime: input.Runtime, Plan: input.Plan, Stage: input.Stage, Rollback: result, RestorePoint: input.RestorePoint})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Passed || len(verification.Findings) != 1 || verification.Findings[0].Code != rollbackPostconditionFindingCode {
		t.Fatalf("VerifyRollback=%#v", verification)
	}
}

func TestRestoreProviderVerifyRestoredUsesFrozenBaseline(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 2)
	result, err := definition.Rollback(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	provider := &restorePointProvider{rollbackInspector: fake}
	beforeCalls := len(fake.restoreCalls)
	verification, err := provider.VerifyRestored(context.Background(), input.RestorePoint, result)
	if err != nil || !verification.Passed {
		t.Fatalf("VerifyRestored=%#v err=%v", verification, err)
	}
	if len(fake.restoreCalls) != beforeCalls {
		t.Fatal("restore-provider verification duplicated rollback mutation")
	}
	if _, err := provider.Restore(context.Background(), input.RestorePoint, input.Apply); !errors.Is(err, errApplyMechanismUnverified) {
		t.Fatalf("restore provider mutation boundary err=%v", err)
	}
}

func TestRollbackReceiptCorrelationRejectsTampering(t *testing.T) {
	fake := newRollbackFixtureAdapter()
	definition, input := rollbackInput(t, fake, 2)
	result, err := definition.Rollback(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeRollbackStageReceipt(result)
	if err != nil {
		t.Fatal(err)
	}
	receipt.NodeID = "other-node"
	payload, _ := json.Marshal(receipt)
	result.State.Payload = payload
	if _, err := definition.VerifyRollback(context.Background(), operation.VerifyRollbackInput{Runtime: input.Runtime, Plan: input.Plan, Stage: input.Stage, Rollback: result, RestorePoint: input.RestorePoint}); err == nil {
		t.Fatal("tampered rollback receipt was accepted")
	}
}

func TestRecoveryArtifactContractCanonicalization(t *testing.T) {
	plan := restorePlan(t)
	point := rollbackRestorePoint(t, plan, 10)
	manifest, err := decodeRestoreManifest(point)
	if err != nil {
		t.Fatal(err)
	}
	if !sort.SliceIsSorted(manifest.Recovery.Artifacts, func(left, right int) bool {
		if manifest.Recovery.Artifacts[left].Kind != manifest.Recovery.Artifacts[right].Kind {
			return manifest.Recovery.Artifacts[left].Kind < manifest.Recovery.Artifacts[right].Kind
		}
		return manifest.Recovery.Artifacts[left].SourcePath < manifest.Recovery.Artifacts[right].SourcePath
	}) {
		t.Fatal("recovery artifacts are not canonicalized")
	}
	copyValue := *manifest.Recovery
	copyValue.Owner.ParticipantNodeIDs = append([]string(nil), copyValue.Owner.ParticipantNodeIDs...)
	if !reflect.DeepEqual(copyValue, *manifest.Recovery) {
		t.Fatal("recovery contract copy changed identity")
	}
}
