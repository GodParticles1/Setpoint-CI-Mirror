package clickhouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/operation"
)

const (
	ExchangeRestorePlanSchema     = "clickhouse.exchange_restore_plan.v1"
	ExchangeRestoreManifestSchema = "clickhouse.exchange_restore_manifest.v3"
	ExchangeRestoreProviderID     = "clickhouse.atomic_exchange.v2"
)

type ExchangeRestorePlan struct {
	Pair  PairParameters        `json:"pair"`
	Items []ExchangeRestoreItem `json:"items"`
}

type ExchangeRestoreItem struct {
	Chunk       TransferChunk `json:"chunk"`
	TargetTable Table         `json:"target_table"`
}

type ExchangeRestoreBaseline struct {
	Key            LedgerKey             `json:"key"`
	OwnershipToken string                `json:"ownership_token"`
	Target         RestoreObjectIdentity `json:"target"`
	Restore        RestoreObjectIdentity `json:"restore"`
	Fingerprint    DataFingerprint       `json:"fingerprint"`
	Partitions     []Partition           `json:"partitions,omitempty"`
}

type ExchangeRestoreManifest struct {
	Plan      ExchangeRestorePlan       `json:"plan"`
	Baselines []ExchangeRestoreBaseline `json:"baselines"`
}

func EncodeExchangeRestorePlan(plan ExchangeRestorePlan) (operation.Artifact, error) {
	payload, err := json.Marshal(plan)
	if err != nil {
		return operation.Artifact{}, fmt.Errorf("encode ClickHouse restore plan: %w", err)
	}
	return operation.Artifact{SchemaVersion: ExchangeRestorePlanSchema, Payload: payload}, nil
}

func decodeExchangeRestorePlan(artifact operation.Artifact) (ExchangeRestorePlan, error) {
	if artifact.SchemaVersion != ExchangeRestorePlanSchema {
		return ExchangeRestorePlan{}, fmt.Errorf("unsupported ClickHouse restore plan schema %q", artifact.SchemaVersion)
	}
	var plan ExchangeRestorePlan
	if err := json.Unmarshal(artifact.Payload, &plan); err != nil {
		return ExchangeRestorePlan{}, fmt.Errorf("decode ClickHouse restore plan: %w", err)
	}
	if len(plan.Items) == 0 {
		return ExchangeRestorePlan{}, errors.New("ClickHouse restore plan has no items")
	}
	return plan, nil
}

type ExchangeRestoreProvider struct {
	ledger   LedgerStore
	store    RestoreStore
	staging  StagingController
	objects  RestoreObjectController
	verifier FingerprintVerifier
	commit   *AtomicExchangeCommitEngine
	now      func() time.Time
	newToken func() (string, error)
}

type exchangeRestoreCandidate struct {
	item        ExchangeRestoreItem
	target      RestoreObjectSnapshot
	fingerprint DataFingerprint
}

func NewExchangeRestoreProvider(ledger LedgerStore, store RestoreStore, staging StagingController, objects RestoreObjectController, verifier FingerprintVerifier, commit *AtomicExchangeCommitEngine) (*ExchangeRestoreProvider, error) {
	if ledger == nil || store == nil || staging == nil || objects == nil || verifier == nil || commit == nil {
		return nil, errors.New("ledger, restore store, staging, restore objects, verifier and commit engine are required")
	}
	return &ExchangeRestoreProvider{
		ledger: ledger, store: store, staging: staging, objects: objects, verifier: verifier, commit: commit,
		now: func() time.Time { return time.Now().UTC() }, newToken: newRestoreOwnershipToken,
	}, nil
}

func (provider *ExchangeRestoreProvider) ID() string { return ExchangeRestoreProviderID }

func (provider *ExchangeRestoreProvider) Create(ctx context.Context, request operation.RestorePointRequest) (operation.RestorePoint, error) {
	if request.OperationID != OperationID {
		return operation.RestorePoint{}, fmt.Errorf("restore provider only supports %s", OperationID)
	}
	if request.RunID == "" {
		return operation.RestorePoint{}, errors.New("restore point run ID is required")
	}
	plan, err := decodeExchangeRestorePlan(request.Plan.Execution)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	pair, err := normalizePairParameters(plan.Pair)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	plan.Pair = pair
	for index := range plan.Items {
		item := &plan.Items[index]
		item.Chunk.RunID = request.RunID
		staging, err := BuildStagingTableName(request.RunID, item.Chunk.TargetTable)
		if err != nil {
			return operation.RestorePoint{}, fmt.Errorf("materialize restore-point staging ownership: %w", err)
		}
		item.Chunk.StagingTable = staging
		if err := validateExchangeCommitRequest(ExchangeCommitRequest{Pair: plan.Pair, Chunk: item.Chunk, TargetTable: item.TargetTable}); err != nil {
			return operation.RestorePoint{}, fmt.Errorf("materialize restore-point execution item: %w", err)
		}
	}
	candidates := make([]exchangeRestoreCandidate, 0, len(plan.Items))
	seen := make(map[LedgerKey]struct{}, len(plan.Items))
	for _, item := range plan.Items {
		key := ledgerKeyForChunk(item.Chunk)
		if _, duplicate := seen[key]; duplicate {
			return operation.RestorePoint{}, fmt.Errorf("duplicate ClickHouse restore target %s.%s", key.Database, key.Table)
		}
		seen[key] = struct{}{}
		capability, err := InspectCommitCapability(ctx, provider.commit.client, pair.Target, item.Chunk.TargetDatabase, item.Chunk.TargetTable)
		if err != nil {
			return operation.RestorePoint{}, err
		}
		if !capability.ExchangeTables {
			return operation.RestorePoint{}, fmt.Errorf("cannot create exchange restore point for %s.%s: %s", item.Chunk.TargetDatabase, item.Chunk.TargetTable, capability.Reason)
		}
		targetSnapshot, err := provider.objects.Inspect(ctx, pair.Target, item.Chunk.TargetDatabase, item.Chunk.TargetTable)
		if err != nil {
			return operation.RestorePoint{}, fmt.Errorf("inspect target for restore point: %w", err)
		}
		if !targetSnapshot.Exists {
			return operation.RestorePoint{}, fmt.Errorf("restore point target %s.%s does not exist", item.Chunk.TargetDatabase, item.Chunk.TargetTable)
		}
		expectedSchema, err := tableSchemaFingerprint(item.TargetTable)
		if err != nil {
			return operation.RestorePoint{}, err
		}
		if targetSnapshot.Identity.SchemaFingerprint != expectedSchema {
			return operation.RestorePoint{}, fmt.Errorf("target schema changed after planning for %s.%s", item.Chunk.TargetDatabase, item.Chunk.TargetTable)
		}
		fingerprint, err := provider.verifier.Fingerprint(ctx, pair.Target, item.Chunk.TargetDatabase, item.TargetTable, nil)
		if err != nil {
			return operation.RestorePoint{}, fmt.Errorf("fingerprint target for restore point: %w", err)
		}
		candidates = append(candidates, exchangeRestoreCandidate{item: item, target: targetSnapshot, fingerprint: fingerprint})
	}
	if len(candidates) != 1 {
		for _, candidate := range candidates {
			if candidate.fingerprint.Rows > 0 {
				return operation.RestorePoint{}, errors.New("bounded non-empty ClickHouse restore points support exactly one target table")
			}
		}
	}

	manifest := ExchangeRestoreManifest{Plan: plan}
	for _, candidate := range candidates {
		record, err := provider.ensureRestoreRecord(ctx, request.RunID, pair.Target, candidate.item, candidate.target, candidate.fingerprint)
		if err != nil {
			return operation.RestorePoint{}, err
		}
		manifest.Baselines = append(manifest.Baselines, ExchangeRestoreBaseline{
			Key: ledgerKeyForChunk(candidate.item.Chunk), OwnershipToken: record.OwnershipToken, Target: record.Target, Restore: record.Restore,
			Fingerprint: record.Baseline, Partitions: append([]Partition(nil), record.Partitions...),
		})
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return operation.RestorePoint{}, err
	}
	createdAt := provider.now()
	sum := sha256.Sum256([]byte(request.RunID + "|" + ExchangeRestoreProviderID))
	point := operation.RestorePoint{ID: "ch-rp-" + hex.EncodeToString(sum[:])[:16], ProviderID: ExchangeRestoreProviderID, OperationID: request.OperationID, RunID: request.RunID, Status: operation.RestorePointVerified, Targets: append([]operation.Target(nil), request.Targets...), CreatedAt: createdAt, Manifest: operation.Artifact{SchemaVersion: ExchangeRestoreManifestSchema, Payload: payload}}
	if request.Retention > 0 {
		expires := createdAt.Add(request.Retention)
		point.ExpiresAt = &expires
	}
	if err := operation.ValidateRestorePoint(point, createdAt); err != nil {
		return operation.RestorePoint{}, err
	}
	if _, err := provider.decodeManifest(point); err != nil {
		return operation.RestorePoint{}, fmt.Errorf("validate created ClickHouse restore manifest: %w", err)
	}
	return point, nil
}

func (provider *ExchangeRestoreProvider) Verify(ctx context.Context, point operation.RestorePoint) (operation.Verification, error) {
	manifest, err := provider.decodeManifest(point)
	if err != nil {
		return operation.Verification{}, err
	}
	for _, item := range manifest.Plan.Items {
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return operation.Verification{}, errors.New("ClickHouse restore baseline is missing")
		}
		record, err := provider.loadBoundRestoreRecord(ctx, point.RunID, item, baseline)
		if err != nil {
			return operation.Verification{}, err
		}
		if record.State != RestoreReady {
			return operation.Verification{Passed: false, Summary: "ClickHouse restore record is not ready"}, nil
		}
		targetSnapshot, err := provider.objects.Inspect(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.Chunk.TargetTable)
		if err != nil {
			return operation.Verification{}, err
		}
		if !targetSnapshot.Exists || targetSnapshot.Identity != baseline.Target {
			return operation.Verification{Passed: false, Summary: "ClickHouse target identity changed after restore point creation"}, nil
		}
		fingerprint, err := provider.verifier.Fingerprint(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.TargetTable, nil)
		if err != nil {
			return operation.Verification{}, err
		}
		if !matchesTargetBaseline(baseline.Fingerprint, fingerprint) {
			return operation.Verification{Passed: false, Summary: "ClickHouse target changed after restore point creation", Findings: []operation.Finding{{Code: "TARGET_CHANGED_AFTER_RESTORE_POINT", Severity: operation.FindingBlocking, Summary: fmt.Sprintf("%s.%s no longer matches its frozen restore baseline", item.Chunk.TargetDatabase, item.Chunk.TargetTable)}}}, nil
		}
		if baseline.Fingerprint.Rows > 0 {
			restoreSnapshot, err := provider.objects.Inspect(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, baseline.Restore.Table)
			if err != nil {
				return operation.Verification{}, err
			}
			if !restoreSnapshot.Exists || restoreSnapshot.Identity != baseline.Restore {
				return operation.Verification{Passed: false, Summary: "ClickHouse restore object identity is not verified"}, nil
			}
			restoreFingerprint, err := provider.verifier.Fingerprint(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, restoreTableMetadata(item.TargetTable, baseline.Restore.Table), nil)
			if err != nil {
				return operation.Verification{}, err
			}
			if !CompareFingerprints(baseline.Fingerprint, restoreFingerprint).Passed {
				return operation.Verification{Passed: false, Summary: "ClickHouse restore object does not match the frozen target baseline"}, nil
			}
		}
	}
	return operation.Verification{Passed: true, Summary: "ClickHouse target and run-owned restore baselines verified"}, nil
}

func (provider *ExchangeRestoreProvider) Restore(ctx context.Context, point operation.RestorePoint, _ operation.ApplyResult) (operation.RollbackResult, error) {
	manifest, err := provider.decodeManifest(point)
	if err != nil {
		return operation.RollbackResult{}, err
	}
	if _, err := provider.loadBoundRestoreRecords(ctx, point.RunID, manifest); err != nil {
		return operation.RollbackResult{}, err
	}
	for _, item := range manifest.Plan.Items {
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return operation.RollbackResult{}, errors.New("ClickHouse restore baseline is missing")
		}
		commitRequest := ExchangeCommitRequest{Pair: manifest.Plan.Pair, Chunk: item.Chunk, TargetTable: item.TargetTable, TargetBaseline: &baseline.Fingerprint}
		key := ledgerKeyForChunk(item.Chunk)
		entry, exists, err := provider.ledger.Get(ctx, key)
		if err != nil {
			return operation.RollbackResult{}, err
		}
		if !exists {
			continue
		}
		if entry.State == LedgerCommitUnknown {
			reconciled, reconcileErr := provider.commit.Reconcile(ctx, commitRequest)
			if reconcileErr != nil {
				return operation.RollbackResult{}, reconcileErr
			}
			entry = reconciled.Entry
		}
		if entry.State == LedgerRollbackPending && entry.Checkpoint != "staging_drop_pending" {
			reconciled, reconcileErr := provider.commit.ReconcileRollback(ctx, commitRequest)
			if reconcileErr != nil {
				return operation.RollbackResult{}, reconcileErr
			}
			entry = reconciled.Entry
		}
		switch entry.State {
		case LedgerCommitted:
			if _, err := provider.commit.Rollback(ctx, commitRequest); err != nil {
				return operation.RollbackResult{}, err
			}
		case LedgerRolledBack:
			continue
		case LedgerRollbackBlocked, LedgerRollbackFailed:
			return operation.RollbackResult{}, fmt.Errorf("automatic rollback is unavailable for %s.%s in state %s", key.Database, key.Table, entry.State)
		default:
			if err := provider.rollbackPrecommitStaging(ctx, entry, manifest.Plan.Pair.Target, item.Chunk); err != nil {
				return operation.RollbackResult{}, err
			}
		}
	}
	payload, _ := json.Marshal(map[string]string{"status": "restored"})
	return operation.RollbackResult{Restored: true, Checkpoint: "clickhouse_exchange_restore_verified", State: operation.Artifact{SchemaVersion: "clickhouse.rollback.v1", Payload: payload}}, nil
}

func (provider *ExchangeRestoreProvider) VerifyRestored(ctx context.Context, point operation.RestorePoint, rollback operation.RollbackResult) (operation.Verification, error) {
	if !rollback.Restored {
		return operation.Verification{Passed: false, Summary: "rollback result is not marked restored"}, nil
	}
	manifest, err := provider.decodeManifest(point)
	if err != nil {
		return operation.Verification{}, err
	}
	for _, item := range manifest.Plan.Items {
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return operation.Verification{}, errors.New("ClickHouse restore baseline is missing")
		}
		record, err := provider.loadBoundRestoreRecord(ctx, point.RunID, item, baseline)
		if err != nil {
			return operation.Verification{}, err
		}
		targetSnapshot, err := provider.objects.Inspect(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.Chunk.TargetTable)
		if err != nil {
			return operation.Verification{}, err
		}
		if !targetSnapshot.Exists || targetSnapshot.Identity != baseline.Target || !samePartitions(targetSnapshot.Partitions, baseline.Partitions) {
			return operation.Verification{Passed: false, Summary: "ClickHouse rollback did not restore the original target identity and partition state"}, nil
		}
		fingerprint, err := provider.verifier.Fingerprint(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.TargetTable, nil)
		if err != nil {
			return operation.Verification{}, err
		}
		if !matchesTargetBaseline(baseline.Fingerprint, fingerprint) {
			return operation.Verification{Passed: false, Summary: "ClickHouse rollback did not restore the original target fingerprint"}, nil
		}
		entry, exists, err := provider.ledger.Get(ctx, ledgerKeyForChunk(item.Chunk))
		if err != nil {
			return operation.Verification{}, err
		}
		if exists && entry.State != LedgerRolledBack {
			return operation.Verification{Passed: false, Summary: fmt.Sprintf("ClickHouse rollback ledger is %s", entry.State)}, nil
		}
		if baseline.Fingerprint.Rows > 0 {
			if record.State == RestoreCleanupPending || record.State == RestoreCleaned {
				continue
			}
			if record.State != RestoreReady {
				return operation.Verification{Passed: false, Summary: fmt.Sprintf("ClickHouse restore record is %s before rollback cleanup", record.State)}, nil
			}
			restoreSnapshot, err := provider.objects.Inspect(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, baseline.Restore.Table)
			if err != nil {
				return operation.Verification{}, err
			}
			if !restoreSnapshot.Exists || restoreSnapshot.Identity != baseline.Restore {
				return operation.Verification{Passed: false, Summary: "ClickHouse restore object was removed before rollback verification"}, nil
			}
			restoreFingerprint, err := provider.verifier.Fingerprint(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, restoreTableMetadata(item.TargetTable, baseline.Restore.Table), nil)
			if err != nil {
				return operation.Verification{}, err
			}
			if !CompareFingerprints(baseline.Fingerprint, restoreFingerprint).Passed {
				return operation.Verification{Passed: false, Summary: "ClickHouse restore object fingerprint changed before cleanup"}, nil
			}
		}
	}
	if err := provider.cleanupVerifiedRestore(ctx, point.RunID, manifest); err != nil {
		return operation.Verification{}, err
	}
	return operation.Verification{Passed: true, Summary: "ClickHouse rollback restored the frozen target baseline and cleaned run-owned objects"}, nil
}

func (provider *ExchangeRestoreProvider) decodeManifest(point operation.RestorePoint) (ExchangeRestoreManifest, error) {
	if point.ProviderID != ExchangeRestoreProviderID || point.Manifest.SchemaVersion != ExchangeRestoreManifestSchema {
		return ExchangeRestoreManifest{}, errors.New("unsupported ClickHouse restore point")
	}
	if err := operation.ValidateRestorePoint(point, provider.now()); err != nil {
		return ExchangeRestoreManifest{}, err
	}
	var manifest ExchangeRestoreManifest
	if err := json.Unmarshal(point.Manifest.Payload, &manifest); err != nil {
		return ExchangeRestoreManifest{}, fmt.Errorf("decode ClickHouse restore manifest: %w", err)
	}
	pair, err := normalizePairParameters(manifest.Plan.Pair)
	if err != nil {
		return ExchangeRestoreManifest{}, fmt.Errorf("validate ClickHouse restore manifest pair: %w", err)
	}
	manifest.Plan.Pair = pair
	if len(manifest.Plan.Items) == 0 || len(manifest.Baselines) != len(manifest.Plan.Items) {
		return ExchangeRestoreManifest{}, errors.New("ClickHouse restore manifest requires one baseline per execution item")
	}
	targets := make(map[string]struct{}, len(point.Targets))
	for _, target := range point.Targets {
		if target.Component == "clickhouse" {
			targets[target.Resource] = struct{}{}
		}
	}
	baselineByKey := make(map[LedgerKey]ExchangeRestoreBaseline, len(manifest.Baselines))
	for _, baseline := range manifest.Baselines {
		if _, duplicate := baselineByKey[baseline.Key]; duplicate {
			return ExchangeRestoreManifest{}, fmt.Errorf("duplicate ClickHouse restore baseline for %#v", baseline.Key)
		}
		if baseline.Target.Database != baseline.Key.Database || baseline.Target.Table != baseline.Key.Table || baseline.Target.Engine == "" || baseline.Target.SchemaFingerprint == "" {
			return ExchangeRestoreManifest{}, fmt.Errorf("ClickHouse restore baseline target identity is invalid for %s.%s", baseline.Key.Database, baseline.Key.Table)
		}
		expectedRestore, err := BuildRestoreTableName(point.RunID, baseline.Key.Table, baseline.OwnershipToken)
		if err != nil {
			return ExchangeRestoreManifest{}, err
		}
		if baseline.Restore.Database != baseline.Key.Database || baseline.Restore.Table != expectedRestore {
			return ExchangeRestoreManifest{}, fmt.Errorf("ClickHouse restore object %q is not owned by run %q", baseline.Restore.Table, point.RunID)
		}
		if baseline.Fingerprint.Rows > 0 && (baseline.Fingerprint.HashSum64 == "" || baseline.Fingerprint.HashXor64 == "" || baseline.Restore.UUID == "" || baseline.Restore.SchemaFingerprint == "") {
			return ExchangeRestoreManifest{}, fmt.Errorf("non-empty ClickHouse restore baseline for %s.%s is incomplete", baseline.Key.Database, baseline.Key.Table)
		}
		baselineByKey[baseline.Key] = baseline
	}
	seenItems := make(map[LedgerKey]struct{}, len(manifest.Plan.Items))
	for _, item := range manifest.Plan.Items {
		if item.Chunk.RunID != point.RunID {
			return ExchangeRestoreManifest{}, fmt.Errorf("ClickHouse restore manifest run ID %q does not match restore point run %q", item.Chunk.RunID, point.RunID)
		}
		expectedStaging, err := BuildStagingTableName(point.RunID, item.Chunk.TargetTable)
		if err != nil {
			return ExchangeRestoreManifest{}, fmt.Errorf("derive ClickHouse restore manifest staging ownership: %w", err)
		}
		if item.Chunk.StagingTable != expectedStaging {
			return ExchangeRestoreManifest{}, fmt.Errorf("ClickHouse restore manifest staging %q is not owned by run %q", item.Chunk.StagingTable, point.RunID)
		}
		if err := validateExchangeCommitRequest(ExchangeCommitRequest{Pair: manifest.Plan.Pair, Chunk: item.Chunk, TargetTable: item.TargetTable}); err != nil {
			return ExchangeRestoreManifest{}, fmt.Errorf("validate ClickHouse restore execution item: %w", err)
		}
		key := ledgerKeyForChunk(item.Chunk)
		if _, duplicate := seenItems[key]; duplicate {
			return ExchangeRestoreManifest{}, fmt.Errorf("duplicate ClickHouse restore execution item for %#v", key)
		}
		seenItems[key] = struct{}{}
		if _, ok := baselineByKey[key]; !ok {
			return ExchangeRestoreManifest{}, fmt.Errorf("ClickHouse restore manifest is missing baseline for %s.%s", key.Database, key.Table)
		}
		resource := item.Chunk.TargetDatabase + "." + item.Chunk.TargetTable
		if _, ok := targets[resource]; !ok {
			return ExchangeRestoreManifest{}, fmt.Errorf("ClickHouse restore execution item %s is outside restore point targets", resource)
		}
	}
	return manifest, nil
}

func (provider *ExchangeRestoreProvider) loadBoundRestoreRecord(ctx context.Context, runID string, item ExchangeRestoreItem, baseline ExchangeRestoreBaseline) (RestoreRecord, error) {
	key := restoreKeyForItem(runID, item)
	record, exists, err := provider.store.GetRestore(ctx, key)
	if err != nil {
		return RestoreRecord{}, err
	}
	if !exists {
		return RestoreRecord{}, errors.New("ClickHouse durable restore record is missing")
	}
	if record.OwnershipToken != baseline.OwnershipToken || record.Target != baseline.Target || record.Restore != baseline.Restore || record.Baseline != baseline.Fingerprint || !samePartitions(record.Partitions, baseline.Partitions) {
		return RestoreRecord{}, errors.New("ClickHouse restore manifest does not match the durable restore record")
	}
	return record, nil
}

func (provider *ExchangeRestoreProvider) loadBoundRestoreRecords(ctx context.Context, runID string, manifest ExchangeRestoreManifest) (map[RestoreKey]RestoreRecord, error) {
	records := make(map[RestoreKey]RestoreRecord, len(manifest.Plan.Items))
	for _, item := range manifest.Plan.Items {
		baseline, ok := restoreBaselineForItem(manifest, item)
		if !ok {
			return nil, errors.New("ClickHouse restore baseline is missing")
		}
		record, err := provider.loadBoundRestoreRecord(ctx, runID, item, baseline)
		if err != nil {
			return nil, err
		}
		records[restoreKeyForItem(runID, item)] = record
	}
	return records, nil
}

func (provider *ExchangeRestoreProvider) ensureRestoreRecord(ctx context.Context, runID string, endpoint Endpoint, item ExchangeRestoreItem, target RestoreObjectSnapshot, baseline DataFingerprint) (RestoreRecord, error) {
	key := restoreKeyForItem(runID, item)
	record, exists, err := provider.store.GetRestore(ctx, key)
	if err != nil {
		return RestoreRecord{}, err
	}
	if !exists {
		ownershipToken, err := provider.newToken()
		if err != nil {
			return RestoreRecord{}, err
		}
		restoreTable, err := BuildRestoreTableName(runID, item.Chunk.TargetTable, ownershipToken)
		if err != nil {
			return RestoreRecord{}, err
		}
		now := provider.now()
		record = RestoreRecord{
			Key: key, State: RestoreIntent, OwnershipToken: ownershipToken, Target: target.Identity,
			Restore: RestoreObjectIdentity{Database: key.Database, Table: restoreTable}, Baseline: baseline,
			Partitions: append([]Partition(nil), target.Partitions...), CreatedAt: now, UpdatedAt: now,
		}
		if err := provider.store.PutRestore(ctx, record); err != nil {
			return RestoreRecord{}, fmt.Errorf("persist ClickHouse restore-point intent: %w", err)
		}
	} else {
		if record.Target != target.Identity || !matchesTargetBaseline(record.Baseline, baseline) || !samePartitions(record.Partitions, target.Partitions) {
			return RestoreRecord{}, errors.New("ClickHouse target changed after restore-point intent was persisted")
		}
		if record.State == RestoreCleaned {
			return RestoreRecord{}, errors.New("ClickHouse restore point is already cleaned; use a new run ID")
		}
		if record.State == RestoreManualReview || record.State == RestoreCleanupPending {
			return RestoreRecord{}, fmt.Errorf("ClickHouse restore point requires reconciliation from state %s", record.State)
		}
	}

	if record.Baseline.Rows == 0 {
		if record.State == RestoreIntent {
			record.State = RestoreReady
			record.Restore.Engine = record.Target.Engine
			record.Restore.SchemaFingerprint = record.Target.SchemaFingerprint
			record.UpdatedAt = provider.now()
			if err := provider.store.PutRestore(ctx, record); err != nil {
				return RestoreRecord{}, fmt.Errorf("persist empty-target restore readiness: %w", err)
			}
		}
		return record, nil
	}

	restoreSnapshot, err := provider.objects.Inspect(ctx, endpoint, record.Restore.Database, record.Restore.Table)
	if err != nil {
		return RestoreRecord{}, err
	}
	if record.State == RestoreReady {
		if err := provider.verifyRestoreObject(ctx, endpoint, item, record, restoreSnapshot); err != nil {
			return RestoreRecord{}, err
		}
		return record, nil
	}
	if record.State == RestoreIntent {
		if restoreSnapshot.Exists {
			cause := errors.New("run-owned ClickHouse restore object name was occupied before run-owned CREATE began")
			return RestoreRecord{}, provider.markRestoreManualReview(ctx, record, cause)
		}
		record.State = RestoreCreating
		record.UpdatedAt = provider.now()
		if err := provider.store.PutRestore(ctx, record); err != nil {
			return RestoreRecord{}, fmt.Errorf("persist ClickHouse restore-object create intent: %w", err)
		}
	}
	if record.State != RestoreCreating {
		return RestoreRecord{}, fmt.Errorf("ClickHouse restore object creation is blocked in state %s", record.State)
	}
	if restoreSnapshot.Exists {
		if _, err := provider.proveRestoreObject(ctx, endpoint, item, record, restoreSnapshot); err != nil {
			cause := fmt.Errorf("object at the run-owned restore name cannot be proven owned after restart; preserving it for manual review: %w", err)
			return RestoreRecord{}, provider.markRestoreManualReview(ctx, record, cause)
		}
	}
	if !restoreSnapshot.Exists {
		if err := provider.objects.Create(ctx, endpoint, record.Restore.Database, record.Restore.Table, item.TargetTable); err != nil {
			return RestoreRecord{}, err
		}
		restoreSnapshot, err = provider.objects.Inspect(ctx, endpoint, record.Restore.Database, record.Restore.Table)
		if err != nil {
			return RestoreRecord{}, err
		}
	}
	identity, err := provider.proveRestoreObject(ctx, endpoint, item, record, restoreSnapshot)
	if err != nil {
		cause := fmt.Errorf("run-owned ClickHouse restore object cannot be proven after create; preserving it for manual review: %w", err)
		return RestoreRecord{}, provider.markRestoreManualReview(ctx, record, cause)
	}
	if record.Restore.UUID == "" {
		record.Restore = identity
		record.LastError = ""
		record.UpdatedAt = provider.now()
		if err := provider.store.PutRestore(ctx, record); err != nil {
			return RestoreRecord{}, fmt.Errorf("persist generated ClickHouse restore-object identity: %w", err)
		}
	}
	record.State = RestoreReady
	record.LastError = ""
	record.UpdatedAt = provider.now()
	if err := provider.store.PutRestore(ctx, record); err != nil {
		return RestoreRecord{}, fmt.Errorf("persist verified ClickHouse restore-point readiness: %w", err)
	}
	return record, nil
}

func (provider *ExchangeRestoreProvider) verifyRestoreObject(ctx context.Context, endpoint Endpoint, item ExchangeRestoreItem, record RestoreRecord, snapshot RestoreObjectSnapshot) error {
	identity, err := provider.proveRestoreObject(ctx, endpoint, item, record, snapshot)
	if err != nil {
		return err
	}
	if record.Restore.UUID == "" || identity != record.Restore {
		return errors.New("run-owned ClickHouse restore object identity is not frozen")
	}
	return nil
}

func (provider *ExchangeRestoreProvider) proveRestoreObject(ctx context.Context, endpoint Endpoint, item ExchangeRestoreItem, record RestoreRecord, snapshot RestoreObjectSnapshot) (RestoreObjectIdentity, error) {
	if !snapshot.Exists {
		return RestoreObjectIdentity{}, errors.New("run-owned ClickHouse restore object is absent")
	}
	if snapshot.Identity.Database != record.Restore.Database || snapshot.Identity.Table != record.Restore.Table || snapshot.Identity.UUID == "" {
		return RestoreObjectIdentity{}, errors.New("run-owned ClickHouse restore object identity is incomplete")
	}
	if record.Restore.UUID != "" && snapshot.Identity.UUID != record.Restore.UUID {
		return RestoreObjectIdentity{}, errors.New("run-owned ClickHouse restore object UUID changed")
	}
	if snapshot.Identity.Engine != record.Target.Engine || snapshot.Identity.SchemaFingerprint != record.Target.SchemaFingerprint {
		return RestoreObjectIdentity{}, errors.New("run-owned ClickHouse restore object schema does not match the target baseline")
	}
	fingerprint, err := provider.verifier.Fingerprint(ctx, endpoint, record.Restore.Database, restoreTableMetadata(item.TargetTable, record.Restore.Table), nil)
	if err != nil {
		return RestoreObjectIdentity{}, err
	}
	if !CompareFingerprints(record.Baseline, fingerprint).Passed {
		return RestoreObjectIdentity{}, errors.New("run-owned ClickHouse restore object fingerprint does not match the target baseline")
	}
	return snapshot.Identity, nil
}

func (provider *ExchangeRestoreProvider) markRestoreManualReview(ctx context.Context, record RestoreRecord, cause error) error {
	record.State = RestoreManualReview
	record.LastError = cause.Error()
	record.UpdatedAt = provider.now()
	if err := provider.store.PutRestore(ctx, record); err != nil {
		return errors.Join(cause, fmt.Errorf("persist ClickHouse restore manual-review state: %w", err))
	}
	return cause
}

func (provider *ExchangeRestoreProvider) cleanupVerifiedRestore(ctx context.Context, runID string, manifest ExchangeRestoreManifest) error {
	inspector, ok := provider.staging.(StagingInspector)
	if !ok {
		return errors.New("staging implementation cannot verify successful restore cleanup")
	}
	records, err := provider.loadBoundRestoreRecords(ctx, runID, manifest)
	if err != nil {
		return err
	}
	for _, record := range records {
		switch record.State {
		case RestoreReady, RestoreCleanupPending, RestoreCleaned:
		default:
			return fmt.Errorf("ClickHouse restore cleanup is blocked in state %s", record.State)
		}
	}
	for _, item := range manifest.Plan.Items {
		baseline, found := restoreBaselineForItem(manifest, item)
		if !found {
			return errors.New("ClickHouse restore baseline is missing")
		}
		record := records[restoreKeyForItem(runID, item)]
		if record.State == RestoreCleaned {
			continue
		}
		if record.State == RestoreReady {
			record.State = RestoreCleanupPending
			record.UpdatedAt = provider.now()
			if err := provider.store.PutRestore(ctx, record); err != nil {
				return fmt.Errorf("persist ClickHouse restore cleanup intent: %w", err)
			}
		}
		if record.State != RestoreCleanupPending {
			return fmt.Errorf("ClickHouse restore cleanup is blocked in state %s", record.State)
		}
		stagingExists, err := inspector.Exists(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.Chunk.StagingTable)
		if err != nil {
			return err
		}
		if stagingExists {
			if err := provider.staging.Drop(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.Chunk.StagingTable); err != nil {
				return err
			}
			stagingExists, err = inspector.Exists(ctx, manifest.Plan.Pair.Target, item.Chunk.TargetDatabase, item.Chunk.StagingTable)
			if err != nil || stagingExists {
				return errors.Join(err, errors.New("run-owned staging table remains after verified rollback cleanup"))
			}
		}
		if baseline.Fingerprint.Rows > 0 {
			restoreSnapshot, err := provider.objects.Inspect(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, baseline.Restore.Table)
			if err != nil {
				return err
			}
			if restoreSnapshot.Exists {
				if restoreSnapshot.Identity != baseline.Restore {
					return errors.New("run-owned restore object identity changed before cleanup")
				}
				if err := provider.objects.Drop(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, baseline.Restore.Table); err != nil {
					return err
				}
				restoreSnapshot, err = provider.objects.Inspect(ctx, manifest.Plan.Pair.Target, baseline.Restore.Database, baseline.Restore.Table)
				if err != nil || restoreSnapshot.Exists {
					return errors.Join(err, errors.New("run-owned restore object remains after cleanup"))
				}
			}
		}
		record.State = RestoreCleaned
		record.UpdatedAt = provider.now()
		if err := provider.store.PutRestore(ctx, record); err != nil {
			return fmt.Errorf("persist verified ClickHouse restore cleanup: %w", err)
		}
	}
	return nil
}

func restoreBaselineForItem(manifest ExchangeRestoreManifest, item ExchangeRestoreItem) (ExchangeRestoreBaseline, bool) {
	key := ledgerKeyForChunk(item.Chunk)
	for _, baseline := range manifest.Baselines {
		if baseline.Key == key {
			return baseline, true
		}
	}
	return ExchangeRestoreBaseline{}, false
}

func restoreKeyForItem(runID string, item ExchangeRestoreItem) RestoreKey {
	return RestoreKey{RunID: runID, Database: item.Chunk.TargetDatabase, Table: item.Chunk.TargetTable}
}

func restoreTableMetadata(target Table, name string) Table {
	target.Name = name
	target.UUID = ""
	return target
}

func samePartitions(left, right []Partition) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (provider *ExchangeRestoreProvider) rollbackPrecommitStaging(ctx context.Context, entry LedgerEntry, endpoint Endpoint, chunk TransferChunk) error {
	cause := errors.New("restore-point pre-commit rollback requested")
	result, err := reconcileNativeStagingCleanup(ctx, provider.ledger, provider.staging, provider.now, entry, endpoint, chunk, cause)
	if result.Entry.State == LedgerRolledBack && errors.Is(err, cause) {
		return nil
	}
	return err
}

func ledgerKeyForChunk(chunk TransferChunk) LedgerKey {
	return LedgerKey{RunID: chunk.RunID, Database: chunk.TargetDatabase, Table: chunk.TargetTable, Partition: chunk.Partition, Chunk: chunk.Sequence}
}

var _ operation.RestorePointProvider = (*ExchangeRestoreProvider)(nil)
