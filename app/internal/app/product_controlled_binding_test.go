package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operation/sysctlrepair"
	"setpoint/internal/operationrun"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	"setpoint/internal/remediation"
	"setpoint/internal/task"
)

// Test-only catalog projection and binding: production metadata is never changed.
type controlledCatalog []plugin.CheckMetadata

func (c controlledCatalog) ListDefinitions() []plugin.CheckMetadata { return c }

func controlledBatchFixture(t *testing.T) *productBatchFixture {
	t.Helper()
	f := newProductBatchFixture(t)
	definitions := controlledCatalog(f.checks.ListDefinitions())
	for i := range definitions {
		if definitions[i].ID == batchCheckA || definitions[i].ID == batchCheckB {
			definitions[i].Remediation.Disposition = plugin.RemediationControlled
		}
	}
	f.product.remediations = definitions
	binding := controlledTestBinding()
	registry, err := remediation.NewRegistry(binding)
	if err != nil {
		t.Fatal(err)
	}
	f.product.bindings = registry
	return f
}

type changedSource struct {
	batchCheckRunRepository
	mutate func(*checkrun.Resource)
}

func (s changedSource) GetCheckRun(ctx context.Context, id string) (checkrun.Resource, error) {
	run, err := s.batchCheckRunRepository.GetCheckRun(ctx, id)
	if err == nil {
		s.mutate(&run)
	}
	return run, err
}

type changedChild struct {
	OperationRunRepository
	mutate func(*operationrun.Resource)
}

func (s changedChild) GetOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	run, err := s.OperationRunRepository.GetOperationRun(ctx, id)
	if err == nil {
		s.mutate(&run)
	}
	return run, err
}

func TestControlledBatchRequiresExplicitConfirmationAndKeepsLifecycle(t *testing.T) {
	f := controlledBatchFixture(t)
	source, err := f.store.GetCheckRun(f.ctx, f.checkRunID)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]plugin.RemediationMetadata{}
	for _, d := range f.product.remediations.ListDefinitions() {
		metadata[d.ID] = d.Remediation
	}
	offers := checkrun.BuildRemediationOffers(source, metadata, f.product.bindings)
	if len(offers) != 2 {
		t.Fatalf("offers=%#v", offers)
	}
	for _, offer := range offers {
		if offer.Availability != "actionable" || offer.SupportsAutomaticFix || offer.Disposition != "CONTROLLED" {
			t.Fatalf("offer=%#v", offer)
		}
	}
	for _, id := range f.runs {
		run, err := f.store.GetOperationRun(f.ctx, id)
		if err != nil || run.Status.State != operation.StateAwaitingConfirm {
			t.Fatalf("ran before confirm: %s %v", run.Status.State, err)
		}
		if _, found, err := f.store.CurrentLeaseByOwner(f.ctx, id); err != nil || found {
			t.Fatalf("lease before confirm=%v %v", found, err)
		}
	}
	request := f.request("controlled-batch", "controlled-confirm", batchCheckA)
	response, err := f.product.ConfirmOperationBatch(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Runs) != 1 || response.Runs[0].Status.State != operation.StateCreatingRestorePoint {
		t.Fatalf("response=%#v", response)
	}
	queued, err := f.store.GetTask(f.ctx, response.Runs[0].Status.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Spec.OperationExecution == nil || queued.Spec.OperationExecution.Action != task.OperationActionCreateRestorePoint {
		t.Fatalf("skipped restore point: %#v", queued.Spec)
	}
}

func TestControlledBatchRevalidatesServerFactsAndConfirmsZeroOnMismatch(t *testing.T) {
	tests := []struct {
		name    string
		source  func(*checkrun.Resource)
		child   func(*operationrun.Resource)
		request func(*protocol.ConfirmOperationBatchRequest)
		setup   func(*productBatchFixture)
	}{
		{name: "source_missing", source: func(r *checkrun.Resource) { r.Tasks = nil }},
		{name: "source_not_unsafe", source: func(r *checkrun.Resource) { r.Tasks[0].Result.Items[0].Status = task.ItemSafe }},
		{name: "source_wrong_shape", source: func(r *checkrun.Resource) { r.Tasks[0].Result.Items[0].CurrentValue = "runtime=1; persisted=1" }},
		{name: "source_duplicate_finding", source: func(r *checkrun.Resource) {
			r.Tasks[0].Result.Items = append(r.Tasks[0].Result.Items, r.Tasks[0].Result.Items[0])
		}},
		{name: "source_node_changed", source: func(r *checkrun.Resource) { r.Tasks[0].Spec.NodeID = "other-node" }},
		{name: "connection_unreviewed", source: func(r *checkrun.Resource) { r.Tasks[0].Result.Items[0].MayAffectConnection = true }},
		{name: "business_unreviewed", source: func(r *checkrun.Resource) { r.Tasks[0].Result.Items[0].MayAffectBusiness = true }},
		{name: "operation_changed", child: func(r *operationrun.Resource) { r.Spec.OperationID = "other.operation" }},
		{name: "node_changed", child: func(r *operationrun.Resource) { r.Spec.NodeID = "other-node" }},
		{name: "targets_changed", child: func(r *operationrun.Resource) {
			r.Spec.Targets = []operation.Target{{Kind: operation.TargetNode, NodeID: "other-node"}}
		}},
		{name: "parameters_changed", child: func(r *operationrun.Resource) {
			r.Spec.Parameters = json.RawMessage(`{"check_id":"net.ipv4.conf.default.accept_redirects.persisted","target_value":"runtime=0; persisted=0"}`)
		}},
		{name: "plan_digest_changed", child: func(r *operationrun.Resource) { r.PlanDigest = "sha256:changed" }},
		{name: "not_awaiting_confirm", child: func(r *operationrun.Resource) { r.Status.State = operation.StateDraft }},
		{name: "forged_finding", request: func(r *protocol.ConfirmOperationBatchRequest) { r.Members[0].CheckID = "forged.check" }},
		{name: "forged_node", request: func(r *protocol.ConfirmOperationBatchRequest) { r.Members[0].NodeID = "forged-node" }},
		{name: "forged_digest", request: func(r *protocol.ConfirmOperationBatchRequest) { r.Members[0].PlanDigest = "sha256:forged" }},
		{name: "invalid_constructed_parameters", setup: func(f *productBatchFixture) {
			b := controlledTestBinding()
			b.Parameters = func(task.CheckItem) (map[string]string, error) {
				return map[string]string{"check_id": batchCheckA, "target_value": "invalid"}, nil
			}
			r, err := remediation.NewRegistry(b)
			if err != nil {
				t.Fatal(err)
			}
			f.product.bindings = r
		}},
		{name: "binding_removed", setup: func(f *productBatchFixture) { f.product.bindings = nil }},
		{name: "metadata_binding_removed", setup: func(f *productBatchFixture) {
			defs := controlledCatalog(f.product.remediations.ListDefinitions())
			for i := range defs {
				defs[i].Remediation.OperationID = ""
			}
			f.product.remediations = defs
		}},
		{name: "apply_unavailable", setup: func(f *productBatchFixture) {
			resolver, err := NewProductExecutionResolver(ProductExecutionCapability{OperationID: sysctlrepair.ID, ApplyAvailable: false, BlockCode: "test_unavailable"})
			if err != nil {
				t.Fatal(err)
			}
			f.product.execution = resolver
		}},
		{name: "capability_missing", setup: func(f *productBatchFixture) {
			resolver, err := NewProductExecutionResolver()
			if err != nil {
				t.Fatal(err)
			}
			f.product.execution = resolver
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := controlledBatchFixture(t)
			if tt.source != nil {
				f.product.checkRuns = changedSource{f.store, tt.source}
			}
			if tt.child != nil {
				f.base.runs = changedChild{f.store, tt.child}
			}
			if tt.setup != nil {
				tt.setup(f)
			}
			request := f.request("controlled-reject", "controlled-reject-confirm", batchCheckA, batchCheckB)
			if tt.request != nil {
				tt.request(&request)
			}
			if _, err := f.product.ConfirmOperationBatch(f.ctx, request); !errors.Is(err, ErrOperationBatchStaleMembership) {
				t.Fatalf("expected fail closed, err=%v", err)
			}
			for _, id := range f.runs {
				r, err := f.store.GetOperationRun(f.ctx, id)
				if err != nil || r.Status.State != operation.StateAwaitingConfirm {
					t.Fatalf("partial confirm: %s %v", r.Status.State, err)
				}
				if _, found, err := f.store.CurrentLeaseByOwner(f.ctx, id); err != nil || found {
					t.Fatalf("unexpected lease=%v %v", found, err)
				}
			}
			if _, err := f.store.GetOperationBatchConfirmationByKey(f.ctx, request.ConfirmationIdempotencyKey); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("persisted invalid authorization: %v", err)
			}
		})
	}
}

func controlledTestBinding() remediation.Binding {
	return remediation.Binding{CheckIDs: []string{batchCheckA, batchCheckB}, OperationID: sysctlrepair.ID, Disposition: plugin.RemediationControlled, SupportsRollback: true, Parameters: func(item task.CheckItem) (map[string]string, error) {
		if item.CurrentValue != "runtime=1; persisted=0" || item.RecommendedValue != "runtime=0; persisted=0" {
			return nil, errors.New("invalid test finding")
		}
		return map[string]string{"check_id": item.ID, "target_value": item.RecommendedValue}, nil
	}}
}

func TestControlledBatchNormalizesServerConstructedParameters(t *testing.T) {
	f := controlledBatchFixture(t)
	binding := controlledTestBinding()
	binding.Impact = remediation.Impact{Connection: true, Business: true}
	original := binding.Parameters
	binding.Parameters = func(item task.CheckItem) (map[string]string, error) {
		params, err := original(item)
		if err != nil {
			return nil, err
		}
		params["check_id"] = " " + params["check_id"] + " "
		params["target_value"] = " " + params["target_value"] + " "
		return params, nil
	}
	registry, err := remediation.NewRegistry(binding)
	if err != nil {
		t.Fatal(err)
	}
	f.product.bindings = registry
	f.product.checkRuns = changedSource{f.store, func(r *checkrun.Resource) {
		r.Tasks[0].Result.Items[0].MayAffectConnection = true
		r.Tasks[0].Result.Items[0].MayAffectBusiness = true
	}}
	if _, err := f.product.ConfirmOperationBatch(f.ctx, f.request("controlled-normalized", "controlled-normalized-confirm", batchCheckA)); err != nil {
		t.Fatal(err)
	}
}
