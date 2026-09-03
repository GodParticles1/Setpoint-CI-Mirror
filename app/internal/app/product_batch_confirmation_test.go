package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operation/sysctlrepair"
	"setpoint/internal/operationbatch"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

const batchCheckA = "net.ipv4.conf.all.accept_redirects.persisted"
const batchCheckB = "net.ipv4.conf.default.accept_redirects.persisted"

type productBatchFixture struct {
	ctx         context.Context
	store       *storage.Store
	checks      *plugin.CheckRegistry
	base        *OperationsService
	product     *ProductOperations
	supervisor  *operation.LeaseSupervisor
	checkRunID  string
	checkTaskID string
	runs        map[string]string
	digests     map[string]string
	now         time.Time
}

func newProductBatchFixture(t *testing.T) *productBatchFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.RegisterNode(ctx, domain.Registration{AgentID: "node-1", Hostname: "node-1", OS: "linux", OSVersion: "test", Arch: "amd64", AgentVersion: "test", ReceivedAt: now}); err != nil {
		t.Fatal(err)
	}
	checks := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(checks); err != nil {
		t.Fatal(err)
	}
	checkService, err := NewService(store, store, checks, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	checkService.now = func() time.Time { return now }
	if err := checkService.SyncChecks(ctx); err != nil {
		t.Fatal(err)
	}
	checkRequest := protocol.CreateCheckRunRequest{APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun", Metadata: protocol.CreateCheckRunMetadata{IdempotencyKey: "batch-source-1", Name: "batch source"}, Spec: protocol.CreateCheckRunSpec{NodeIDs: []string{"node-1"}, CheckIDs: []string{batchCheckA, batchCheckB}, Parameters: map[string]json.RawMessage{}}}
	checkRun, created, err := checkService.CreateCheckRun(ctx, checkRequest)
	if err != nil || !created || len(checkRun.Tasks) != 1 {
		t.Fatalf("check run=%#v created=%v err=%v", checkRun, created, err)
	}
	checkTask := checkRun.Tasks[0]
	claimed, err := store.ClaimTask(ctx, "node-1", "batch-check-claim", now.Add(time.Second))
	if err != nil || claimed == nil || claimed.Metadata.ID != checkTask.Metadata.ID {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, err := store.AcknowledgeTask(ctx, "node-1", claimed.Metadata.ID, "batch-check-claim", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	unsafe := false
	items := []task.CheckItem{}
	for _, checkID := range []string{batchCheckA, batchCheckB} {
		items = append(items, task.CheckItem{ID: checkID, Status: task.ItemUnsafe, Name: checkID, CurrentValue: "runtime=1; persisted=0", RecommendedValue: "runtime=0; persisted=0", Compliant: &unsafe, Risk: "low", Remediation: "Apply validated runtime target", EvidenceSummary: "unsafe runtime with safe persistence", Applicable: true, SupportsAutomaticFix: true, SupportsRollback: true, ExecutedAt: now.Add(3 * time.Second)})
	}
	checkResult := &task.CheckResult{PluginID: checkTask.Spec.PluginID, PluginVersion: "test", State: task.CheckCompleted, StartedAt: now.Add(2 * time.Second), CompletedAt: now.Add(3 * time.Second), Items: items}
	if _, err := store.CompleteTask(ctx, "node-1", claimed.Metadata.ID, task.ResultSubmission{ClaimID: "batch-check-claim", Phase: task.PhaseSucceeded, Result: checkResult}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	catalog := operation.NewRegistry()
	if err := catalog.Register(sysctlrepair.NewCatalogDescriptor()); err != nil {
		t.Fatal(err)
	}
	base, err := NewOperationsService(store, store, catalog, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base.now = func() time.Time { return now.Add(4 * time.Second) }
	resolver, err := NewProductExecutionResolver(ProductExecutionCapability{OperationID: sysctlrepair.ID, ApplyAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := operation.NewLeaseSupervisor(store, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(supervisor.Close)
	product, err := NewProductOperationsWithBatchRemediation(base, store, supervisor, resolver, checks)
	if err != nil {
		t.Fatal(err)
	}
	product.now = func() time.Time { return now.Add(5 * time.Second) }
	fixture := &productBatchFixture{ctx: ctx, store: store, checks: checks, base: base, product: product, supervisor: supervisor, checkRunID: checkRun.Metadata.ID, checkTaskID: checkTask.Metadata.ID, runs: map[string]string{}, digests: map[string]string{}, now: now}
	for index, checkID := range []string{batchCheckA, batchCheckB} {
		fixture.createPlannedChild(t, checkID, index)
	}
	return fixture
}

func (fixture *productBatchFixture) createPlannedChild(t *testing.T, checkID string, index int) {
	t.Helper()
	request := protocol.CreateOperationRunRequest{APIVersion: "setpoint.io/v1", Kind: "OperationRun"}
	request.Metadata.IdempotencyKey = "batch-child-" + checkID
	request.Spec.OperationID = sysctlrepair.ID
	request.Spec.NodeID = "node-1"
	request.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	request.Spec.Parameters = json.RawMessage(`{"check_id":"` + checkID + `","target_value":"runtime=0; persisted=0"}`)
	run, created, err := fixture.base.CreateOperationRun(fixture.ctx, request)
	if err != nil || !created {
		t.Fatalf("create child %s: created=%v err=%v", checkID, created, err)
	}
	claimID := "planning-claim-" + checkID
	claimed, err := fixture.store.ClaimTask(fixture.ctx, "node-1", claimID, fixture.now.Add(time.Duration(10+index)*time.Second))
	if err != nil || claimed == nil || claimed.Metadata.ID != run.Status.TaskID {
		t.Fatalf("claim child %s task=%#v err=%v", checkID, claimed, err)
	}
	if _, err := fixture.store.AcknowledgeTask(fixture.ctx, "node-1", claimed.Metadata.ID, claimID, fixture.now.Add(time.Duration(20+index)*time.Second)); err != nil {
		t.Fatal(err)
	}
	plan := operation.Plan{SchemaVersion: "setpoint.operation.plan.v1", Summary: "repair " + checkID, Steps: []operation.PlanStep{{ID: "repair", Target: operation.Target{Kind: operation.TargetNode, NodeID: "node-1"}, Writes: true}}, Execution: operation.Artifact{SchemaVersion: "test.execution.v1", Payload: json.RawMessage(`{"check_id":"` + checkID + `"}`)}}
	impact := operation.Impact{Summary: "bounded runtime repair", Risk: operation.RiskLow, Changes: []operation.Change{}}
	digest := "sha256:plan-" + checkID
	planning := operation.PlanningResult{OperationID: run.Spec.OperationID, OperationVersion: run.Spec.OperationVersion, CapabilityDigest: run.Spec.CapabilityDigest, State: operation.StateAwaitingConfirm, Checkpoint: "plan_ready", StartedAt: fixture.now.Add(time.Duration(20+index) * time.Second), CompletedAt: fixture.now.Add(time.Duration(30+index) * time.Second), Plan: &plan, Impact: &impact, PlanDigest: digest}
	if _, err := fixture.store.CompleteTask(fixture.ctx, "node-1", claimed.Metadata.ID, task.ResultSubmission{ClaimID: claimID, Phase: task.PhaseSucceeded, OperationResult: &planning}, planning.CompletedAt); err != nil {
		t.Fatal(err)
	}
	fixture.runs[checkID] = run.Metadata.ID
	fixture.digests[checkID] = digest
}

func (fixture *productBatchFixture) request(batchID, key string, checks ...string) protocol.ConfirmOperationBatchRequest {
	request := protocol.ConfirmOperationBatchRequest{BatchID: batchID, SourceCheckRunID: fixture.checkRunID, ConfirmationIdempotencyKey: key}
	for _, checkID := range checks {
		request.Members = append(request.Members, struct {
			TaskID     string `json:"task_id"`
			CheckID    string `json:"check_id"`
			NodeID     string `json:"node_id"`
			RunID      string `json:"run_id"`
			PlanDigest string `json:"plan_digest"`
		}{TaskID: fixture.checkTaskID, CheckID: checkID, NodeID: "node-1", RunID: fixture.runs[checkID], PlanDigest: fixture.digests[checkID]})
	}
	return request
}

func TestBatchConfirmationStaleChildConfirmsZeroNewChildren(t *testing.T) {
	fixture := newProductBatchFixture(t)
	request := fixture.request("batch-stale", "batch-stale-confirm", batchCheckA, batchCheckB)
	request.Members[1].PlanDigest = "sha256:stale"
	if _, err := fixture.product.ConfirmOperationBatch(fixture.ctx, request); !errors.Is(err, ErrOperationBatchStaleMembership) {
		t.Fatalf("stale error=%v", err)
	}
	for _, checkID := range []string{batchCheckA, batchCheckB} {
		run, err := fixture.store.GetOperationRun(fixture.ctx, fixture.runs[checkID])
		if err != nil || run.Status.State != operation.StateAwaitingConfirm {
			t.Fatalf("child %s state=%s err=%v", checkID, run.Status.State, err)
		}
		if _, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, run.Metadata.ID); err != nil || found {
			t.Fatalf("child %s lease found=%v err=%v", checkID, found, err)
		}
	}
	if _, err := fixture.store.GetOperationBatchConfirmationByKey(fixture.ctx, "batch-stale-confirm"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("receipt err=%v", err)
	}
}

func TestBatchConfirmationSerializesSameNodeAndIsConcurrentIdempotent(t *testing.T) {
	fixture := newProductBatchFixture(t)
	request := fixture.request("batch-concurrent", "batch-concurrent-confirm", batchCheckA, batchCheckB)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fixture.product.ConfirmOperationBatch(fixture.ctx, request)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := fixture.store.GetOperationBatchConfirmationByKey(fixture.ctx, request.ConfirmationIdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Members) != 2 || receipt.Members[0].State != operationbatch.MemberConfirmed || receipt.Members[1].State != operationbatch.MemberPending {
		t.Fatalf("receipt=%#v", receipt)
	}
	first, _ := fixture.store.GetOperationRun(fixture.ctx, fixture.runs[batchCheckA])
	second, _ := fixture.store.GetOperationRun(fixture.ctx, fixture.runs[batchCheckB])
	if first.Status.State != operation.StateCreatingRestorePoint || second.Status.State != operation.StateAwaitingConfirm {
		t.Fatalf("first=%s second=%s", first.Status.State, second.Status.State)
	}
	before, err := fixture.store.ListTasks(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.product.ConfirmOperationBatch(fixture.ctx, request); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.store.ListTasks(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("duplicate confirm created tasks: before=%d after=%d", len(before), len(after))
	}
	if _, found, err := fixture.store.CurrentLeaseByOwner(fixture.ctx, second.Metadata.ID); err != nil || found {
		t.Fatalf("pending child acquired duplicate lease found=%v err=%v", found, err)
	}
}

func TestBatchConfirmationSameKeyChangedFingerprintConflicts(t *testing.T) {
	fixture := newProductBatchFixture(t)
	request := fixture.request("batch-fingerprint", "batch-fingerprint-confirm", batchCheckA)
	if _, err := fixture.product.ConfirmOperationBatch(fixture.ctx, request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Members = append([]struct {
		TaskID     string `json:"task_id"`
		CheckID    string `json:"check_id"`
		NodeID     string `json:"node_id"`
		RunID      string `json:"run_id"`
		PlanDigest string `json:"plan_digest"`
	}{}, request.Members...)
	changed.Members[0].PlanDigest = "sha256:changed"
	if _, err := fixture.product.ConfirmOperationBatch(fixture.ctx, changed); !errors.Is(err, ErrOperationBatchFingerprintConflict) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestBatchConfirmationRestartResumesPersistedIntentAndCancellationSuppressesPending(t *testing.T) {
	fixture := newProductBatchFixture(t)
	request := fixture.request("batch-restart", "batch-restart-confirm", batchCheckA, batchCheckB)
	members, err := normalizeOperationBatchMembers(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.product.preflightOperationBatch(fixture.ctx, request.SourceCheckRunID, members); err != nil {
		t.Fatal(err)
	}
	receipt, err := operationbatch.NewReceipt(request.BatchID, request.SourceCheckRunID, request.ConfirmationIdempotencyKey, members, fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.store.CreateOrGetOperationBatchConfirmation(fixture.ctx, receipt); err != nil || !created {
		t.Fatalf("persist intent created=%v err=%v", created, err)
	}
	if run, _ := fixture.store.GetOperationRun(fixture.ctx, fixture.runs[batchCheckA]); run.Status.State != operation.StateAwaitingConfirm {
		t.Fatalf("fanout happened before restart: %s", run.Status.State)
	}

	resolver, _ := NewProductExecutionResolver(ProductExecutionCapability{OperationID: sysctlrepair.ID, ApplyAvailable: true})
	restarted, err := NewProductOperationsWithBatchRemediation(fixture.base, fixture.store, fixture.supervisor, resolver, fixture.checks)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return fixture.now.Add(2 * time.Minute) }
	if err := restarted.ResumeBatchConfirmations(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := fixture.store.GetOperationBatchConfirmation(fixture.ctx, request.BatchID)
	if err != nil || restored.Members[0].State != operationbatch.MemberConfirmed || restored.Members[1].State != operationbatch.MemberPending {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	if _, err := restarted.CancelOperationRun(fixture.ctx, fixture.runs[batchCheckB]); err != nil {
		t.Fatal(err)
	}
	restored, err = fixture.store.GetOperationBatchConfirmation(fixture.ctx, request.BatchID)
	if err != nil || restored.Members[1].State != operationbatch.MemberSuppressedCanceled {
		t.Fatalf("suppressed=%#v err=%v", restored, err)
	}
	if err := restarted.ResumeBatchConfirmations(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := fixture.store.GetOperationRun(fixture.ctx, fixture.runs[batchCheckB])
	if second.Status.State != operation.StateCanceledBeforeApply {
		t.Fatalf("canceled child restarted: %s", second.Status.State)
	}
	response, err := restarted.GetOperationBatchConfirmation(fixture.ctx, request.BatchID)
	if err != nil || len(response.Runs) != 2 || response.Runs[0].Plan == nil || response.Runs[0].PlanDigest != fixture.digests[batchCheckA] {
		t.Fatalf("reconstruction=%#v err=%v", response, err)
	}
}
