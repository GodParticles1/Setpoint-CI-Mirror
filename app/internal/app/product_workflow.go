package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/operationbatch"
	"setpoint/internal/operationrun"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

type productOperationRepository interface {
	OperationRunRepository
	GetTask(context.Context, string) (task.Resource, error)
	SaveOperationExecutionCheckpoint(context.Context, string, operation.State, string, operationrun.ExecutionSnapshot, *operationrun.Recovery, operation.JournalEntry, time.Time) (operationrun.Resource, error)
	ContinueOperationRun(context.Context, string, string, operation.State, string, task.Resource, operation.JournalEntry, time.Time) (operationrun.Resource, error)
	List(context.Context, string) ([]operation.JournalEntry, error)
}

type productLeaseSupervisor interface {
	Acquire(context.Context, string, []operation.Target) (operation.LockLease, error)
	Resume(context.Context, string, []operation.Target) (operation.LockLease, error)
	CurrentLeaseByOwner(context.Context, string) (operation.LockLease, bool, error)
	Release(context.Context, string) error
}

type batchConfirmationRepository interface {
	CreateOrGetOperationBatchConfirmation(context.Context, operationbatch.Receipt) (operationbatch.Receipt, bool, error)
	GetOperationBatchConfirmation(context.Context, string) (operationbatch.Receipt, error)
	GetOperationBatchConfirmationByKey(context.Context, string) (operationbatch.Receipt, error)
	ListOperationBatchConfirmationsByCheckRun(context.Context, string, int, int) ([]operationbatch.Receipt, error)
	ListPendingOperationBatchConfirmations(context.Context, int, int) ([]operationbatch.Receipt, error)
	UpdateOperationBatchConfirmationMemberState(context.Context, string, int, operationbatch.MemberState, time.Time) (operationbatch.Receipt, error)
}

type batchCheckRunRepository interface {
	GetCheckRun(context.Context, string) (checkrun.Resource, error)
}

type batchRemediationCatalog interface {
	ListDefinitions() []plugin.CheckMetadata
}

type ProductOperations struct {
	base           *OperationsService
	runs           productOperationRepository
	lease          productLeaseSupervisor
	execution      *ProductExecutionResolver
	batch          batchConfirmationRepository
	checkRuns      batchCheckRunRepository
	remediations   batchRemediationCatalog
	now            func() time.Time
	confirmationMu sync.Mutex
}

func NewProductOperations(base *OperationsService, runs productOperationRepository, lease productLeaseSupervisor, execution *ProductExecutionResolver) (*ProductOperations, error) {
	return newProductOperations(base, runs, lease, execution, nil)
}

func NewProductOperationsWithBatchRemediation(base *OperationsService, runs productOperationRepository, lease productLeaseSupervisor, execution *ProductExecutionResolver, remediations batchRemediationCatalog) (*ProductOperations, error) {
	if remediations == nil {
		return nil, errors.New("batch remediation catalog is required")
	}
	return newProductOperations(base, runs, lease, execution, remediations)
}

func newProductOperations(base *OperationsService, runs productOperationRepository, lease productLeaseSupervisor, execution *ProductExecutionResolver, remediations batchRemediationCatalog) (*ProductOperations, error) {
	if base == nil || runs == nil || lease == nil || execution == nil {
		return nil, errors.New("base operations service, operation repository, lease supervisor and execution resolver are required")
	}
	service := &ProductOperations{base: base, runs: runs, lease: lease, execution: execution, remediations: remediations, now: time.Now}
	if remediations != nil {
		batch, ok := any(runs).(batchConfirmationRepository)
		if !ok {
			return nil, errors.New("operation repository does not provide durable batch confirmation receipts")
		}
		checkRuns, ok := any(runs).(batchCheckRunRepository)
		if !ok {
			return nil, errors.New("operation repository does not provide source CheckRun reconstruction")
		}
		service.batch = batch
		service.checkRuns = checkRuns
	}
	return service, nil
}

func (service *ProductOperations) ListOperations() ([]operationrun.DefinitionResource, error) {
	resources, err := service.base.ListOperations()
	if err != nil {
		return nil, err
	}
	for index := range resources {
		resources[index] = service.productOperationAvailability(resources[index])
	}
	return resources, nil
}

func (service *ProductOperations) GetOperation(id string) (operationrun.DefinitionResource, error) {
	resource, err := service.base.GetOperation(id)
	if err != nil {
		return operationrun.DefinitionResource{}, err
	}
	return service.productOperationAvailability(resource), nil
}

func (service *ProductOperations) productOperationAvailability(resource operationrun.DefinitionResource) operationrun.DefinitionResource {
	capability, ok := service.execution.Resolve(resource.Metadata.ID)
	if !ok {
		resource.Availability.Apply = false
		resource.Availability.BlockCode = OperationExecutionUnavailableBlock
		return resource
	}
	resource.Availability.Apply = capability.ApplyAvailable
	resource.Availability.BlockCode = capability.BlockCode
	return resource
}

func (service *ProductOperations) CreateOperationRun(ctx context.Context, request protocol.CreateOperationRunRequest) (operationrun.Resource, bool, error) {
	run, created, err := service.base.CreateOperationRun(ctx, request)
	return service.decorateOperationRun(run), created, err
}

func (service *ProductOperations) GetOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	run, err := service.base.GetOperationRun(ctx, id)
	return service.decorateOperationRun(run), err
}

func (service *ProductOperations) ListOperationRuns(ctx context.Context, options protocol.ListOptions) ([]operationrun.Resource, protocol.ListOptions, error) {
	runs, normalized, err := service.base.ListOperationRuns(ctx, options)
	for index := range runs {
		runs[index] = service.decorateOperationRun(runs[index])
	}
	return runs, normalized, err
}

func (service *ProductOperations) ConfirmOperationBatch(ctx context.Context, request protocol.ConfirmOperationBatchRequest) (protocol.OperationBatchConfirmationResponse, error) {
	service.confirmationMu.Lock()
	defer service.confirmationMu.Unlock()
	if err := service.requireBatchConfirmationSupport(); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, err
	}
	members, err := normalizeOperationBatchMembers(request)
	if err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: err}
	}
	batchID := strings.TrimSpace(request.BatchID)
	checkRunID := strings.TrimSpace(request.SourceCheckRunID)
	confirmationKey := strings.TrimSpace(request.ConfirmationIdempotencyKey)
	if err := validateIdentifier(batchID); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: fmt.Errorf("batch_id: %w", err)}
	}
	if err := validateIdentifier(checkRunID); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: fmt.Errorf("source_check_run_id: %w", err)}
	}
	if err := validateIdentifier(confirmationKey); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: fmt.Errorf("confirmation_idempotency_key: %w", err)}
	}
	fingerprint, err := operationbatch.Fingerprint(batchID, checkRunID, members)
	if err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: err}
	}
	if existing, err := service.batch.GetOperationBatchConfirmationByKey(ctx, confirmationKey); err == nil {
		if existing.ConfirmationFingerprint != fingerprint || existing.BatchID != batchID || existing.SourceCheckRunID != checkRunID {
			return protocol.OperationBatchConfirmationResponse{}, &ConflictError{Err: ErrOperationBatchFingerprintConflict}
		}
		return service.fanOutBatchConfirmationUnlocked(ctx, existing)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return protocol.OperationBatchConfirmationResponse{}, err
	}

	if err := service.preflightOperationBatch(ctx, checkRunID, members); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, err
	}
	receipt, err := operationbatch.NewReceipt(batchID, checkRunID, confirmationKey, members, service.now().UTC())
	if err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: err}
	}
	persisted, _, err := service.batch.CreateOrGetOperationBatchConfirmation(ctx, receipt)
	if errors.Is(err, operationbatch.ErrFingerprintConflict) {
		return protocol.OperationBatchConfirmationResponse{}, &ConflictError{Err: ErrOperationBatchFingerprintConflict}
	}
	if err != nil {
		return protocol.OperationBatchConfirmationResponse{}, err
	}
	if persisted.ConfirmationFingerprint != fingerprint {
		return protocol.OperationBatchConfirmationResponse{}, &ConflictError{Err: ErrOperationBatchFingerprintConflict}
	}
	return service.fanOutBatchConfirmationUnlocked(ctx, persisted)
}

func (service *ProductOperations) GetOperationBatchConfirmation(ctx context.Context, batchID string) (protocol.OperationBatchConfirmationResponse, error) {
	if err := service.requireBatchConfirmationSupport(); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, err
	}
	batchID = strings.TrimSpace(batchID)
	if err := validateIdentifier(batchID); err != nil {
		return protocol.OperationBatchConfirmationResponse{}, &ValidationError{Err: fmt.Errorf("batch_id: %w", err)}
	}
	receipt, err := service.batch.GetOperationBatchConfirmation(ctx, batchID)
	if err != nil {
		return protocol.OperationBatchConfirmationResponse{}, err
	}
	return service.operationBatchConfirmationResponse(ctx, receipt)
}

func (service *ProductOperations) ListOperationBatchConfirmations(ctx context.Context, checkRunID string, options protocol.ListOptions) ([]protocol.OperationBatchConfirmationResponse, protocol.ListOptions, error) {
	if err := service.requireBatchConfirmationSupport(); err != nil {
		return nil, protocol.ListOptions{}, err
	}
	checkRunID = strings.TrimSpace(checkRunID)
	if err := validateIdentifier(checkRunID); err != nil {
		return nil, protocol.ListOptions{}, &ValidationError{Err: fmt.Errorf("check_run_id: %w", err)}
	}
	options = normalizeListOptions(options)
	receipts, err := service.batch.ListOperationBatchConfirmationsByCheckRun(ctx, checkRunID, options.Limit, options.Offset)
	if err != nil {
		return nil, options, err
	}
	responses := make([]protocol.OperationBatchConfirmationResponse, 0, len(receipts))
	for _, receipt := range receipts {
		response, err := service.operationBatchConfirmationResponse(ctx, receipt)
		if err != nil {
			return nil, options, err
		}
		responses = append(responses, response)
	}
	return responses, options, nil
}

// ResumeBatchConfirmations resumes only durable authorization fan-out. Child
// lifecycle recovery remains owned by ResumeOperationRuns/ContinueOperationRun.
func (service *ProductOperations) ResumeBatchConfirmations(ctx context.Context) error {
	service.confirmationMu.Lock()
	defer service.confirmationMu.Unlock()
	return service.resumeBatchConfirmationsUnlocked(ctx)
}

func (service *ProductOperations) resumeBatchConfirmationsUnlocked(ctx context.Context) error {
	if service.batch == nil {
		return nil
	}
	const pageSize = 100
	var pending []operationbatch.Receipt
	for offset := 0; ; offset += pageSize {
		page, err := service.batch.ListPendingOperationBatchConfirmations(ctx, pageSize, offset)
		if err != nil {
			return err
		}
		pending = append(pending, page...)
		if len(page) < pageSize {
			break
		}
	}
	for _, receipt := range pending {
		if _, err := service.fanOutBatchConfirmationUnlocked(ctx, receipt); err != nil {
			return err
		}
	}
	return nil
}

func (service *ProductOperations) preflightOperationBatch(ctx context.Context, checkRunID string, members []operationbatch.FrozenMember) error {
	source, err := service.checkRuns.GetCheckRun(ctx, checkRunID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return &ConflictError{Err: fmt.Errorf("%w: source CheckRun no longer exists", ErrOperationBatchStaleMembership)}
		}
		return err
	}
	definitions := service.remediations.ListDefinitions()
	remediationMetadata := make(map[string]plugin.RemediationMetadata, len(definitions))
	for _, definition := range definitions {
		remediationMetadata[definition.ID] = definition.Remediation
	}
	offers := checkrun.BuildRemediationOffers(source, remediationMetadata)
	offerByIdentity := make(map[string]checkrun.RemediationOffer, len(offers))
	for _, offer := range offers {
		offerByIdentity[operationBatchIdentityKey(offer.TaskID, offer.CheckID, offer.NodeID)] = offer
	}
	taskByID := make(map[string]task.Resource, len(source.Tasks))
	for _, resource := range source.Tasks {
		taskByID[resource.Metadata.ID] = resource
	}

	for index, member := range members {
		resource, ok := taskByID[member.Identity.TaskID]
		if !ok || resource.Spec.NodeID != member.Identity.NodeID {
			return staleBatchMember(index, "source task/node identity changed")
		}
		matchedFinding := false
		if resource.Result != nil {
			for _, item := range resource.Result.Items {
				if item.ID == member.Identity.CheckID {
					if matchedFinding {
						return staleBatchMember(index, "source finding identity is duplicated")
					}
					matchedFinding = true
					if item.Status != task.ItemUnsafe {
						return staleBatchMember(index, "source finding is no longer unsafe")
					}
				}
			}
		}
		if !matchedFinding {
			return staleBatchMember(index, "source finding no longer exists")
		}
		offer, ok := offerByIdentity[operationBatchIdentityKey(member.Identity.TaskID, member.Identity.CheckID, member.Identity.NodeID)]
		if !ok || offer.CheckRunID != checkRunID || offer.Availability != "actionable" || !offer.SupportsAutomaticFix || !offer.SupportsRollback || offer.OperationID == "" || len(offer.OperationParameters) == 0 {
			return staleBatchMember(index, "Server remediation capability is not currently actionable")
		}
		run, err := service.base.GetOperationRun(ctx, member.RunID)
		if err != nil {
			return staleBatchMember(index, "child OperationRun no longer exists")
		}
		if run.Status.State != operation.StateAwaitingConfirm {
			return staleBatchMember(index, "child OperationRun is not awaiting confirmation")
		}
		if run.PlanDigest == "" || run.PlanDigest != member.PlanDigest {
			return staleBatchMember(index, "child plan_digest changed after preview")
		}
		capability, ok := service.execution.Resolve(run.Spec.OperationID)
		if !ok || !capability.ApplyAvailable || len(run.Spec.SecretRefs) != 0 {
			return staleBatchMember(index, "child execution capability is unavailable")
		}
		if run.Spec.NodeID != member.Identity.NodeID || run.Spec.OperationID != offer.OperationID {
			return staleBatchMember(index, "child OperationRun is not bound to the source finding")
		}
		expectedTargets := []operation.Target{{Kind: operation.TargetNode, NodeID: member.Identity.NodeID}}
		if !reflect.DeepEqual(run.Spec.Targets, expectedTargets) {
			return staleBatchMember(index, "child OperationRun targets differ from the finding binding")
		}
		expectedParameters, err := json.Marshal(offer.OperationParameters)
		if err != nil {
			return err
		}
		expectedParameters, err = service.base.catalog.NormalizeParameters(offer.OperationID, expectedParameters)
		if err != nil {
			return staleBatchMember(index, "Server remediation parameters are no longer valid")
		}
		if !bytes.Equal(run.Spec.Parameters, expectedParameters) {
			return staleBatchMember(index, "child OperationRun parameters differ from the finding binding")
		}
	}
	return nil
}

func (service *ProductOperations) fanOutBatchConfirmationUnlocked(ctx context.Context, receipt operationbatch.Receipt) (protocol.OperationBatchConfirmationResponse, error) {
	current := receipt
	for _, member := range current.Members {
		if member.State != operationbatch.MemberPending {
			continue
		}
		run, err := service.base.GetOperationRun(ctx, member.RunID)
		if err != nil {
			continue
		}
		if run.Status.State == operation.StateCanceledBeforeApply {
			updated, updateErr := service.batch.UpdateOperationBatchConfirmationMemberState(ctx, current.BatchID, member.Ordinal, operationbatch.MemberSuppressedCanceled, service.now().UTC())
			if updateErr != nil {
				return protocol.OperationBatchConfirmationResponse{}, updateErr
			}
			current = updated
			continue
		}
		if run.PlanDigest != member.PlanDigest {
			// The accepted intent is immutable. Never adopt a new digest and never
			// auto-replan a child after acceptance.
			continue
		}
		if run.Status.State == operation.StateAwaitingConfirm {
			_, err = service.confirmOperationRunUnlocked(ctx, run.Metadata.ID, protocol.ConfirmOperationRunRequest{
				IdempotencyKey: batchChildConfirmationKey(current.ConfirmationIdempotencyKey, run.Metadata.ID),
				PlanDigest:     member.PlanDigest,
			})
			if errors.Is(err, operation.ErrLockResourceBusy) {
				// Durable authorization remains pending until the existing child
				// releases the same resource lease.
				continue
			}
			if err != nil {
				continue
			}
		} else if !operationRunWasConfirmed(run.Status.State) {
			continue
		}
		updated, updateErr := service.batch.UpdateOperationBatchConfirmationMemberState(ctx, current.BatchID, member.Ordinal, operationbatch.MemberConfirmed, service.now().UTC())
		if updateErr != nil {
			return protocol.OperationBatchConfirmationResponse{}, updateErr
		}
		current = updated
	}
	latest, err := service.batch.GetOperationBatchConfirmation(ctx, current.BatchID)
	if err != nil {
		return protocol.OperationBatchConfirmationResponse{}, err
	}
	return service.operationBatchConfirmationResponse(ctx, latest)
}

func (service *ProductOperations) operationBatchConfirmationResponse(ctx context.Context, receipt operationbatch.Receipt) (protocol.OperationBatchConfirmationResponse, error) {
	response := protocol.OperationBatchConfirmationResponse{Receipt: receipt, Runs: make([]operationrun.Resource, 0, len(receipt.Members))}
	for _, member := range receipt.Members {
		run, err := service.base.GetOperationRun(ctx, member.RunID)
		if err != nil {
			return protocol.OperationBatchConfirmationResponse{}, err
		}
		response.Runs = append(response.Runs, service.decorateOperationRun(run))
	}
	return response, nil
}

func (service *ProductOperations) requireBatchConfirmationSupport() error {
	if service.batch == nil || service.checkRuns == nil || service.remediations == nil {
		return errors.New("durable operation batch confirmation support is unavailable")
	}
	return nil
}

func normalizeOperationBatchMembers(request protocol.ConfirmOperationBatchRequest) ([]operationbatch.FrozenMember, error) {
	if len(request.Members) == 0 {
		return nil, errors.New("members must not be empty")
	}
	members := make([]operationbatch.FrozenMember, len(request.Members))
	seenIdentity := make(map[string]struct{}, len(request.Members))
	seenRuns := make(map[string]struct{}, len(request.Members))
	for index, requested := range request.Members {
		identity := operationbatch.MemberIdentity{TaskID: strings.TrimSpace(requested.TaskID), CheckID: strings.TrimSpace(requested.CheckID), NodeID: strings.TrimSpace(requested.NodeID)}
		for field, value := range map[string]string{"task_id": identity.TaskID, "check_id": identity.CheckID, "node_id": identity.NodeID, "run_id": strings.TrimSpace(requested.RunID)} {
			if err := validateIdentifier(value); err != nil {
				return nil, fmt.Errorf("members[%d].%s: %w", index, field, err)
			}
		}
		planDigest := strings.TrimSpace(requested.PlanDigest)
		if planDigest == "" || len(planDigest) > 128 {
			return nil, fmt.Errorf("members[%d].plan_digest is required and must not exceed 128 bytes", index)
		}
		identityKey := operationBatchIdentityKey(identity.TaskID, identity.CheckID, identity.NodeID)
		if _, duplicate := seenIdentity[identityKey]; duplicate {
			return nil, fmt.Errorf("members[%d] duplicates an immutable member identity", index)
		}
		runID := strings.TrimSpace(requested.RunID)
		if _, duplicate := seenRuns[runID]; duplicate {
			return nil, fmt.Errorf("members[%d] duplicates a child run_id", index)
		}
		seenIdentity[identityKey] = struct{}{}
		seenRuns[runID] = struct{}{}
		members[index] = operationbatch.FrozenMember{Identity: identity, RunID: runID, PlanDigest: planDigest}
	}
	return members, nil
}

func staleBatchMember(index int, reason string) error {
	return &ConflictError{Err: fmt.Errorf("%w: member %d %s", ErrOperationBatchStaleMembership, index, reason)}
}

func operationBatchIdentityKey(taskID, checkID, nodeID string) string {
	return taskID + "\x00" + checkID + "\x00" + nodeID
}

func batchChildConfirmationKey(confirmationKey, runID string) string {
	digest := sha256.Sum256([]byte(confirmationKey + "\x00" + runID))
	return "batch-confirm:" + hex.EncodeToString(digest[:])
}

func operationRunWasConfirmed(state operation.State) bool {
	switch state {
	case operation.StateQueued, operation.StateAcquiringLock, operation.StateCreatingRestorePoint, operation.StateRunning,
		operation.StateVerifying, operation.StateSucceeded, operation.StateBlocked, operation.StateFailed,
		operation.StateRollingBack, operation.StateRolledBack, operation.StateRollbackFailed, operation.StateInterrupted:
		return true
	default:
		return false
	}
}

func (service *ProductOperations) ConfirmOperationRun(ctx context.Context, id string, request protocol.ConfirmOperationRunRequest) (operationrun.Resource, error) {
	service.confirmationMu.Lock()
	defer service.confirmationMu.Unlock()
	return service.confirmOperationRunUnlocked(ctx, id, request)
}

func (service *ProductOperations) confirmOperationRunUnlocked(ctx context.Context, id string, request protocol.ConfirmOperationRunRequest) (operationrun.Resource, error) {
	if err := validateIdentifier(strings.TrimSpace(request.IdempotencyKey)); err != nil {
		return operationrun.Resource{}, &ValidationError{Err: fmt.Errorf("idempotency_key: %w", err)}
	}
	run, err := service.base.GetOperationRun(ctx, id)
	if err != nil {
		return operationrun.Resource{}, err
	}
	capability, ok := service.execution.Resolve(run.Spec.OperationID)
	if !ok || !capability.ApplyAvailable {
		return operationrun.Resource{}, &ConflictError{Err: ErrOperationExecutionUnavailable}
	}
	if len(run.Spec.SecretRefs) != 0 {
		return operationrun.Resource{}, &ConflictError{Err: ErrSecretDeliveryUnavailable}
	}
	if run.PlanDigest == "" || strings.TrimSpace(request.PlanDigest) != run.PlanDigest {
		return operationrun.Resource{}, &ConflictError{Err: ErrOperationPlanDigestConflict}
	}
	for {
		switch run.Status.State {
		case operation.StateAwaitingConfirm:
			if _, err := service.ensureLease(ctx, run); err != nil {
				return operationrun.Resource{}, err
			}
			run, err = service.advance(ctx, run, operation.StateQueued, "confirmed_and_queued", "operation confirmed; authoritative lease acquired", nil)
		case operation.StateQueued:
			if _, err := service.ensureLease(ctx, run); err != nil {
				return operationrun.Resource{}, err
			}
			run, err = service.advance(ctx, run, operation.StateAcquiringLock, "lease_acquired", "authoritative operation lease is supervised", nil)
		case operation.StateAcquiringLock:
			if _, err := service.ensureLease(ctx, run); err != nil {
				return operationrun.Resource{}, err
			}
			queued, queueErr := service.queueAction(ctx, run, run.Status.TaskID, 0, task.OperationActionCreateRestorePoint, operation.StateCreatingRestorePoint, "create_restore_point_queued")
			return service.decorateOperationRun(queued), queueErr
		case operation.StateCreatingRestorePoint, operation.StateRunning, operation.StateVerifying, operation.StateRollingBack,
			operation.StateSucceeded, operation.StateRolledBack, operation.StateBlocked, operation.StateInterrupted, operation.StateRollbackFailed:
			return service.decorateOperationRun(run), nil
		default:
			return operationrun.Resource{}, &ConflictError{Err: ErrOperationStateConflict}
		}
		if err != nil {
			return operationrun.Resource{}, err
		}
	}
}

func (service *ProductOperations) CancelOperationRun(ctx context.Context, id string) (operationrun.Resource, error) {
	service.confirmationMu.Lock()
	defer service.confirmationMu.Unlock()
	run, err := service.base.CancelOperationRun(ctx, id)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if run.Status.State == operation.StateCanceledBeforeApply {
		if err := service.releaseLeaseIfBoundTaskConverged(ctx, run); err != nil {
			return operationrun.Resource{}, err
		}
	} else if run.Status.Recovery != nil && run.Status.Recovery.Code == operationrun.RecoveryCancellationRequested {
		if err := service.continueOperationRun(ctx, run.Metadata.ID); err != nil {
			return operationrun.Resource{}, err
		}
		run, err = service.base.GetOperationRun(ctx, run.Metadata.ID)
		if err != nil {
			return operationrun.Resource{}, err
		}
	}
	if err := service.resumeBatchConfirmationsUnlocked(ctx); err != nil {
		return operationrun.Resource{}, err
	}
	return service.decorateOperationRun(run), nil
}

func (service *ProductOperations) decorateOperationRun(run operationrun.Resource) operationrun.Resource {
	capability, ok := service.execution.Resolve(run.Spec.OperationID)
	run.Status.ApplyAvailable = ok && capability.ApplyAvailable && len(run.Spec.SecretRefs) == 0
	return run
}

// ContinueOperationRun is called only after the bounded action result is
// durable. It is the Server-owned decision point for at most one next action.
func (service *ProductOperations) ContinueOperationRun(ctx context.Context, runID string) error {
	if err := service.continueOperationRun(ctx, runID); err != nil {
		return err
	}
	service.confirmationMu.Lock()
	defer service.confirmationMu.Unlock()
	return service.resumeBatchConfirmationsUnlocked(ctx)
}

func (service *ProductOperations) continueOperationRun(ctx context.Context, runID string) error {
	run, err := service.runs.GetOperationRun(ctx, runID)
	if err != nil {
		return err
	}
	if operation.Terminal(run.Status.State) {
		return service.releaseLeaseIfBoundTaskConverged(ctx, run)
	}
	capability, ok := service.execution.Resolve(run.Spec.OperationID)
	if !ok || !capability.ApplyAvailable {
		return ErrOperationExecutionUnavailable
	}
	resource, err := service.runs.GetTask(ctx, run.Status.TaskID)
	if err != nil {
		return err
	}
	if resource.Kind != task.KindOperationExecutionTask || resource.Spec.OperationExecution == nil {
		if strings.HasSuffix(run.Status.Checkpoint, "_queued") {
			return nil
		}
		return errors.New("operation continuation requires the durable bounded action task")
	}
	if !task.Terminal(resource.Status.Phase) {
		return nil
	}
	action := resource.Spec.OperationExecution.Action
	contract := resource.Spec.OperationExecution
	if run.Status.Checkpoint != stagedActionResultCheckpoint(*contract, resource.Status.Phase) && run.Status.Checkpoint != stageCheckpoint(run, contract.StageIndex, "reconnect_wait") {
		return nil
	}
	stageIndex := contract.StageIndex
	if run.Status.Recovery != nil && run.Status.Recovery.Code == operationrun.RecoveryCancellationRequested && action == task.OperationActionCreateRestorePoint {
		return service.continueContainedCancellation(ctx, run, resource.Metadata.ID, stageIndex)
	}
	if run.Status.Checkpoint == stageCheckpoint(run, stageIndex, "reconnect_wait") {
		facts, factErr := stageExecutionFacts(run, stageIndex)
		if factErr != nil {
			return factErr
		}
		if facts.ApplyAt.IsZero() {
			return errors.New("reconnect barrier lacks durable Apply terminal evidence")
		}
		node, observeErr := service.base.nodes.GetNode(ctx, contract.Stage.ExecutorNodeID, service.base.offlineAfter)
		if observeErr != nil || !node.LastSeenAt.After(facts.ApplyAt) {
			return nil
		}
		_, err = service.queueAction(ctx, run, resource.Metadata.ID, stageIndex, task.OperationActionVerify, operation.StateVerifying, "verify_queued")
		return err
	}

	if resource.Status.Phase != task.PhaseSucceeded {
		switch action {
		case task.OperationActionCreateRestorePoint:
			if previous, ok := previousAppliedStage(run, stageIndex); ok {
				_, err = service.queueAction(ctx, run, resource.Metadata.ID, previous, task.OperationActionRollback, operation.StateRollingBack, "rollback_queued")
				return err
			}
			_, err = service.advance(ctx, run, operation.StateBlocked, "restore_point_failed", "restore point creation failed before mutation", &operationrun.Recovery{Code: "restore_point_failed", SafeNext: "inspect_and_create_new_run", ManualReview: true})
			if err == nil {
				err = service.releaseLease(ctx, run)
			}
			return err
		case task.OperationActionApply:
			_, err = service.advance(ctx, run, operation.StateInterrupted, "apply_outcome_requires_reconcile", "Apply failed with mutation outcome requiring reconciliation", &operationrun.Recovery{Code: "apply_outcome_uncertain", Checkpoint: run.Status.Checkpoint, SafeNext: "reconcile_before_retry_or_rollback", ManualReview: true})
			return err
		case task.OperationActionVerify:
			facts, factsErr := stageExecutionFacts(run, stageIndex)
			if factsErr != nil || facts.RestorePoint == nil || facts.Apply == nil {
				_, err = service.advance(ctx, run, operation.StateInterrupted, "verify_failed_without_rollback_proof", "verification failed without complete rollback prerequisites", &operationrun.Recovery{Code: "rollback_prerequisites_missing", SafeNext: "reconcile", ManualReview: true})
				return err
			}
			_, err = service.ensureLease(ctx, run)
			if err != nil {
				return err
			}
			_, err = service.queueAction(ctx, run, resource.Metadata.ID, stageIndex, task.OperationActionRollback, operation.StateRollingBack, "rollback_queued")
			return err
		case task.OperationActionRollback, task.OperationActionVerifyRollback:
			_, err = service.advance(ctx, run, operation.StateRollbackFailed, "rollback_failed", "rollback or rollback verification failed", &operationrun.Recovery{Code: "rollback_failed", SafeNext: "manual_recovery", ManualReview: true})
			if err == nil {
				err = service.releaseLease(ctx, run)
			}
			return err
		}
	}

	switch action {
	case task.OperationActionCreateRestorePoint:
		_, err = service.queueAction(ctx, run, resource.Metadata.ID, stageIndex, task.OperationActionApply, operation.StateRunning, "apply_queued")
	case task.OperationActionApply:
		if contract.Stage.Barrier == operation.StageBarrierAgentReconnect {
			_, err = service.advance(ctx, run, operation.StateRunning, stageCheckpoint(run, stageIndex, "reconnect_wait"), "waiting for the same persistent Agent identity after self-readdress", nil)
		} else {
			_, err = service.queueAction(ctx, run, resource.Metadata.ID, stageIndex, task.OperationActionVerify, operation.StateVerifying, "verify_queued")
		}
	case task.OperationActionVerify:
		stages, stageErr := operationrun.ExecutionStages(run)
		if stageErr != nil {
			return stageErr
		}
		if stageIndex+1 < len(stages) {
			_, err = service.queueAction(ctx, run, resource.Metadata.ID, stageIndex+1, task.OperationActionCreateRestorePoint, operation.StateCreatingRestorePoint, "create_restore_point_queued")
		} else {
			_, err = service.advance(ctx, run, operation.StateSucceeded, "verified", "operation verification passed", nil)
			if err == nil {
				err = service.releaseLease(ctx, run)
			}
		}
	case task.OperationActionRollback:
		_, err = service.queueAction(ctx, run, resource.Metadata.ID, stageIndex, task.OperationActionVerifyRollback, operation.StateRollingBack, "verify_rollback_queued")
	case task.OperationActionVerifyRollback:
		if previous, ok := previousAppliedStage(run, stageIndex); ok {
			_, err = service.queueAction(ctx, run, resource.Metadata.ID, previous, task.OperationActionRollback, operation.StateRollingBack, "rollback_queued")
		} else {
			_, err = service.advance(ctx, run, operation.StateRolledBack, "rollback_verified", "rollback verification passed", nil)
			if err == nil {
				err = service.releaseLease(ctx, run)
			}
		}
	default:
		err = fmt.Errorf("unsupported operation continuation action %q", action)
	}
	return err
}

func (service *ProductOperations) continueContainedCancellation(ctx context.Context, run operationrun.Resource, completedTaskID string, stageIndex int) error {
	reconcile := &operationrun.Recovery{
		Code: operationrun.RecoveryCancellationReconcile, Checkpoint: run.Status.Checkpoint,
		SafeNext: "reconcile_before_rollback", ManualReview: true,
	}
	previous, ok := previousAppliedStage(run, stageIndex)
	if !ok {
		_, err := service.advance(ctx, run, operation.StateInterrupted, "cancellation_requires_reconcile", "cancellation found durable Apply evidence without an exact rollback stage", reconcile)
		return err
	}
	if _, err := service.ensureLease(ctx, run); err != nil {
		_, advanceErr := service.advance(ctx, run, operation.StateInterrupted, "cancellation_requires_reconcile", "cancellation could not prove authoritative lease containment for rollback", reconcile)
		if advanceErr != nil {
			return errors.Join(err, advanceErr)
		}
		return nil
	}
	if _, err := service.queueAction(ctx, run, completedTaskID, previous, task.OperationActionRollback, operation.StateRollingBack, "rollback_queued"); err != nil {
		_, advanceErr := service.advance(ctx, run, operation.StateInterrupted, "cancellation_requires_reconcile", "cancellation could not construct exact participant rollback; reconciliation is required", reconcile)
		if advanceErr != nil {
			return errors.Join(err, advanceErr)
		}
		return nil
	}
	return nil
}

// ResumeOperationRuns reconstructs the exact durable Server checkpoint after
// restart. It never replays an interrupted mutation and never creates more
// than the deterministic next bounded action.
func (service *ProductOperations) ResumeOperationRuns(ctx context.Context) error {
	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		runs, err := service.runs.ListOperationRuns(ctx, pageSize, offset)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if operation.Terminal(run.Status.State) {
				if err := service.releaseLeaseIfBoundTaskConverged(ctx, run); err != nil {
					return err
				}
				continue
			}
			capability, ok := service.execution.Resolve(run.Spec.OperationID)
			if !ok || !capability.ApplyAvailable {
				continue
			}
			switch run.Status.State {
			case operation.StateQueued:
				if _, err := service.ensureLease(ctx, run); err != nil {
					return err
				}
				if _, err := service.advance(ctx, run, operation.StateAcquiringLock, "lease_acquired", "authoritative operation lease resumed after Server restart", nil); err != nil {
					return err
				}
			case operation.StateAcquiringLock:
				if _, err := service.ensureLease(ctx, run); err != nil {
					return err
				}
				if _, err := service.queueAction(ctx, run, run.Status.TaskID, 0, task.OperationActionCreateRestorePoint, operation.StateCreatingRestorePoint, "create_restore_point_queued"); err != nil {
					return err
				}
			case operation.StateCreatingRestorePoint, operation.StateRunning, operation.StateVerifying, operation.StateRollingBack:
				if err := service.continueOperationRun(ctx, run.Metadata.ID); err != nil {
					return err
				}
			case operation.StateInterrupted:
				// An uncertain Apply outcome is reconciliation-only and is never replayed.
			}
		}
		if len(runs) < pageSize {
			return nil
		}
	}
}

func (service *ProductOperations) queueAction(ctx context.Context, run operationrun.Resource, completedTaskID string, stageIndex int, action task.OperationAction, state operation.State, checkpoint string) (operationrun.Resource, error) {
	if run.Plan == nil {
		return operationrun.Resource{}, errors.New("operation run has no durable plan")
	}
	stages, err := operationrun.ExecutionStages(run)
	if err != nil {
		return operationrun.Resource{}, err
	}
	if stageIndex < 0 || stageIndex >= len(stages) {
		return operationrun.Resource{}, errors.New("operation stage index is outside the frozen plan")
	}
	stage := stages[stageIndex]
	facts, _ := stageExecutionFacts(run, stageIndex)
	targets := operationrun.StageTargets(run, stage)
	participants := append([]string(nil), run.Spec.ParticipantNodeIDs...)
	if len(participants) == 0 {
		participants = []string{run.Spec.NodeID}
	}
	contract := task.OperationExecutionContract{
		OperationID: run.Spec.OperationID, RunID: run.Metadata.ID, Action: action, PlanDigest: run.PlanDigest,
		ParticipantNodeIDs: participants, StageIndex: stageIndex, Stage: stage,
		Targets: targets, Plan: *run.Plan,
	}
	switch action {
	case task.OperationActionApply:
		if run.Impact == nil || facts.RestorePoint == nil {
			return operationrun.Resource{}, errors.New("Apply continuation requires impact and restore point")
		}
		contract.Impact = run.Impact
		contract.RestorePoint = facts.RestorePoint
	case task.OperationActionVerify:
		if facts.Apply == nil {
			return operationrun.Resource{}, errors.New("verify continuation requires Apply result")
		}
		contract.Apply = facts.Apply
	case task.OperationActionRollback:
		if facts.RestorePoint == nil || facts.Apply == nil {
			return operationrun.Resource{}, errors.New("rollback continuation requires restore point and Apply result")
		}
		contract.RestorePoint = facts.RestorePoint
		contract.Apply = facts.Apply
	case task.OperationActionVerifyRollback:
		if facts.RestorePoint == nil || facts.Rollback == nil {
			return operationrun.Resource{}, errors.New("verify rollback continuation requires restore point and rollback result")
		}
		contract.RestorePoint = facts.RestorePoint
		contract.Rollback = facts.Rollback
	}
	frozen, digest, err := task.NewOperationExecutionContract(contract)
	if err != nil {
		return operationrun.Resource{}, err
	}
	at := service.now().UTC()
	taskID := operationStageTaskID(run, stageIndex, action)
	checkpoint = stageCheckpoint(run, stageIndex, checkpoint)
	next := task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindOperationExecutionTask,
		Metadata: task.Metadata{ID: taskID, IdempotencyKey: taskID, CreatedAt: at},
		Spec: task.Spec{
			NodeID: stage.ExecutorNodeID, OperationExecution: &frozen, ContractDigest: digest,
			OperationID: run.Spec.OperationID, OperationVersion: run.Spec.OperationVersion, CapabilityDigest: run.Spec.CapabilityDigest,
			Targets: append([]operation.Target(nil), frozen.Targets...), Parameters: append([]byte(nil), run.Spec.Parameters...), SecretRefs: append([]operation.SecretRef(nil), run.Spec.SecretRefs...),
		},
		Status: task.Status{Phase: task.PhasePending, UpdatedAt: at},
	}
	sequence, err := service.nextJournalSequence(ctx, run.Metadata.ID)
	if err != nil {
		return operationrun.Resource{}, err
	}
	journal := operation.JournalEntry{RunID: run.Metadata.ID, Sequence: sequence, State: state, Checkpoint: checkpoint, Message: "Server queued bounded action " + string(action), At: at,
		Evidence: []operation.EvidenceRef{{ID: taskID, Kind: "operation_action_task"}, {ID: string(action), Kind: "operation_action"},
			{ID: stage.ID, Kind: "operation_stage"}, {ID: stage.ExecutorNodeID, Kind: "operation_stage_executor"}}}
	return service.runs.ContinueOperationRun(ctx, run.Metadata.ID, completedTaskID, state, checkpoint, next, journal, at)
}

func (service *ProductOperations) advance(ctx context.Context, run operationrun.Resource, state operation.State, checkpoint, message string, recovery *operationrun.Recovery) (operationrun.Resource, error) {
	sequence, err := service.nextJournalSequence(ctx, run.Metadata.ID)
	if err != nil {
		return operationrun.Resource{}, err
	}
	at := service.now().UTC()
	journal := operation.JournalEntry{RunID: run.Metadata.ID, Sequence: sequence, State: state, Checkpoint: checkpoint, Message: message, At: at}
	return service.runs.SaveOperationExecutionCheckpoint(ctx, run.Metadata.ID, state, checkpoint, operationrun.ExecutionSnapshot{}, recovery, journal, at)
}

func (service *ProductOperations) nextJournalSequence(ctx context.Context, runID string) (int64, error) {
	entries, err := service.runs.List(ctx, runID)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 1, nil
	}
	return entries[len(entries)-1].Sequence + 1, nil
}

func (service *ProductOperations) ensureLease(ctx context.Context, run operationrun.Resource) (operation.LockLease, error) {
	targets := operationExecutionTargets(run)
	if lease, found, err := service.lease.CurrentLeaseByOwner(ctx, run.Metadata.ID); err == nil && found {
		resources := make([]operation.LockResource, 0, len(targets))
		for _, target := range targets {
			key, keyErr := operation.ResourceLockKey(target)
			if keyErr != nil {
				return operation.LockLease{}, keyErr
			}
			resources = append(resources, operation.LockResource{Key: key})
		}
		if err := operation.ValidateLeaseCoverage(lease, run.Metadata.ID, resources, service.now().UTC()); err == nil {
			return lease, nil
		}
	}
	if lease, err := service.lease.Resume(ctx, run.Metadata.ID, targets); err == nil {
		return lease, nil
	} else if !errors.Is(err, operation.ErrLeaseAuthoritativeAbsence) {
		return operation.LockLease{}, err
	}
	return service.lease.Acquire(ctx, run.Metadata.ID, targets)
}

func operationExecutionTargets(run operationrun.Resource) []operation.Target {
	targets := make([]operation.Target, 0, len(run.Spec.Targets))
	seen := make(map[operation.Target]struct{})
	appendTarget := func(target operation.Target) {
		if _, exists := seen[target]; exists {
			return
		}
		seen[target] = struct{}{}
		targets = append(targets, target)
	}
	for _, target := range run.Spec.Targets {
		appendTarget(target)
	}
	if run.Plan != nil {
		for _, step := range run.Plan.Steps {
			appendTarget(step.Target)
		}
	}
	return targets
}

func stageExecutionFacts(run operationrun.Resource, stageIndex int) (operationrun.StageExecutionSnapshot, error) {
	if run.Execution != nil {
		for _, stage := range run.Execution.Stages {
			if stage.StageIndex == stageIndex {
				return stage, nil
			}
		}
	}
	stages, err := operationrun.ExecutionStages(run)
	if err != nil || stageIndex < 0 || stageIndex >= len(stages) {
		return operationrun.StageExecutionSnapshot{}, errors.New("operation stage facts do not match the frozen plan")
	}
	facts := operationrun.StageExecutionSnapshot{StageIndex: stageIndex, StageID: stages[stageIndex].ID, ExecutorNodeID: stages[stageIndex].ExecutorNodeID}
	if len(run.Spec.ParticipantNodeIDs) <= 1 && run.Execution != nil {
		facts.RestorePoint = run.Execution.RestorePoint
		facts.Apply = run.Execution.Apply
		facts.Verification = run.Execution.Verification
		facts.Rollback = run.Execution.Rollback
		facts.RollbackVerification = run.Execution.RollbackVerification
	}
	return facts, nil
}

func previousAppliedStage(run operationrun.Resource, before int) (int, bool) {
	for index := before - 1; index >= 0; index-- {
		facts, err := stageExecutionFacts(run, index)
		if err == nil && facts.Apply != nil && facts.RollbackVerification == nil {
			return index, true
		}
	}
	return 0, false
}

func operationStageTaskID(run operationrun.Resource, stageIndex int, action task.OperationAction) string {
	if len(run.Spec.ParticipantNodeIDs) <= 1 {
		return run.Metadata.ID + ":" + string(action)
	}
	return fmt.Sprintf("%s:stage:%d:%s", run.Metadata.ID, stageIndex, action)
}

func stageCheckpoint(run operationrun.Resource, stageIndex int, checkpoint string) string {
	if len(run.Spec.ParticipantNodeIDs) <= 1 {
		return checkpoint
	}
	return fmt.Sprintf("stage_%d_%s", stageIndex, checkpoint)
}

func stagedActionResultCheckpoint(contract task.OperationExecutionContract, phase task.Phase) string {
	checkpoint := "action_" + string(contract.Action) + "_" + string(phase)
	if len(contract.ParticipantNodeIDs) > 1 {
		return fmt.Sprintf("stage_%d_%s", contract.StageIndex, checkpoint)
	}
	return checkpoint
}

func (service *ProductOperations) releaseLease(ctx context.Context, run operationrun.Resource) error {
	return service.lease.Release(ctx, run.Metadata.ID)
}

func (service *ProductOperations) releaseLeaseIfBoundTaskConverged(ctx context.Context, run operationrun.Resource) error {
	if run.Status.State == operation.StateCanceledBeforeApply {
		return service.reconcileCanceledBeforeApplyLease(ctx, run)
	}
	return service.resumeAndReleaseLeaseIfPresent(ctx, run)
}

func (service *ProductOperations) reconcileCanceledBeforeApplyLease(ctx context.Context, run operationrun.Resource) error {
	resource, err := service.runs.GetTask(ctx, run.Status.TaskID)
	if err != nil {
		return err
	}
	if task.Terminal(resource.Status.Phase) {
		return service.resumeAndReleaseLeaseIfPresent(ctx, run)
	}
	if resource.Status.Phase != task.PhaseCancelRequested {
		return errors.New("canceled operation run has a nonterminal task outside cancel_requested")
	}
	if resource.Kind == task.KindOperationPlanningTask {
		if resource.Spec.OperationExecution != nil {
			return errors.New("canceled planning task unexpectedly carries an execution contract")
		}
		return service.resumeAndReleaseLeaseIfPresent(ctx, run)
	}
	if err := validateCanceledRestorePointTask(run, resource); err != nil {
		return err
	}
	_, err = service.ensureLease(ctx, run)
	return err
}

func (service *ProductOperations) resumeAndReleaseLeaseIfPresent(ctx context.Context, run operationrun.Resource) error {
	if _, err := service.lease.Resume(ctx, run.Metadata.ID, operationExecutionTargets(run)); err != nil {
		if errors.Is(err, operation.ErrLeaseAuthoritativeAbsence) {
			return nil
		}
		return err
	}
	return service.releaseLease(ctx, run)
}

func validateCanceledRestorePointTask(run operationrun.Resource, resource task.Resource) error {
	if resource.Kind != task.KindOperationExecutionTask || resource.Spec.OperationExecution == nil {
		return errors.New("canceled operation containment requires an operation execution task")
	}
	contract := *resource.Spec.OperationExecution
	if err := task.ValidateOperationExecutionContract(contract, resource.Spec.ContractDigest); err != nil {
		return fmt.Errorf("validate canceled operation execution contract: %w", err)
	}
	if contract.Action != task.OperationActionCreateRestorePoint || contract.RunID != run.Metadata.ID || contract.OperationID != run.Spec.OperationID || contract.StageIndex != 0 {
		return errors.New("canceled operation execution contract is not the bound initial restore-point action")
	}
	if resource.Metadata.ID != run.Status.TaskID || resource.Metadata.ID != operationStageTaskID(run, contract.StageIndex, contract.Action) {
		return errors.New("canceled operation run is not bound to its deterministic restore-point task")
	}
	if run.Plan == nil || contract.PlanDigest != run.PlanDigest || !reflect.DeepEqual(contract.Plan, *run.Plan) {
		return errors.New("canceled restore-point task does not match the confirmed plan")
	}
	stages, err := operationrun.ExecutionStages(run)
	if err != nil || len(stages) == 0 || !reflect.DeepEqual(contract.Stage, stages[0]) {
		return errors.New("canceled restore-point task stage differs from the frozen operation stage")
	}
	participants := append([]string(nil), run.Spec.ParticipantNodeIDs...)
	if len(participants) == 0 {
		participants = []string{run.Spec.NodeID}
	}
	if !reflect.DeepEqual(contract.ParticipantNodeIDs, participants) {
		return errors.New("canceled restore-point task participants differ from the frozen operation participants")
	}
	targets := operationrun.StageTargets(run, stages[0])
	if !reflect.DeepEqual(contract.Targets, targets) || !reflect.DeepEqual(resource.Spec.Targets, targets) {
		return errors.New("canceled restore-point task targets differ from the frozen operation stage targets")
	}
	if resource.Spec.NodeID != stages[0].ExecutorNodeID || resource.Spec.OperationID != run.Spec.OperationID ||
		resource.Spec.OperationVersion != run.Spec.OperationVersion || resource.Spec.CapabilityDigest != run.Spec.CapabilityDigest {
		return errors.New("canceled restore-point task identity differs from the frozen operation run")
	}
	if !bytes.Equal(resource.Spec.Parameters, run.Spec.Parameters) ||
		!reflect.DeepEqual(append([]operation.SecretRef{}, resource.Spec.SecretRefs...), append([]operation.SecretRef{}, run.Spec.SecretRefs...)) {
		return errors.New("canceled restore-point task inputs differ from the frozen operation run")
	}
	return nil
}

type operationContinuation interface {
	ContinueOperationRun(context.Context, string) error
}

type ProductService struct {
	*Service
	continuation operationContinuation
}

func NewProductService(base *Service, continuation operationContinuation) (*ProductService, error) {
	if base == nil || continuation == nil {
		return nil, errors.New("base service and operation continuation are required")
	}
	return &ProductService{Service: base, continuation: continuation}, nil
}

func (service *ProductService) SubmitTaskResult(ctx context.Context, agentID, taskID string, submission task.ResultSubmission) (task.Resource, error) {
	existing, err := service.nodes.GetTask(ctx, strings.TrimSpace(taskID))
	if err != nil || existing.Kind != task.KindOperationExecutionTask {
		return service.Service.SubmitTaskResult(ctx, agentID, taskID, submission)
	}
	if err := validateTaskAgentRequest(agentID, taskID, submission.ClaimID); err != nil {
		return task.Resource{}, &ValidationError{Err: err}
	}
	if existing.Spec.OperationExecution == nil || submission.OperationExecutionResult == nil || submission.Result != nil || submission.OperationResult != nil {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution task requires exactly one bounded action result")}
	}
	if !task.ValidResultPhase(submission.Phase) {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution result phase must be terminal")}
	}
	result := submission.OperationExecutionResult
	contract := existing.Spec.OperationExecution
	if result.OperationID != contract.OperationID || result.RunID != contract.RunID || result.Action != contract.Action {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution result does not match the frozen bounded action contract")}
	}
	if len(contract.ParticipantNodeIDs) > 0 && (result.StageID != contract.Stage.ID || result.StageIndex != contract.StageIndex || result.ExecutorNodeID != contract.Stage.ExecutorNodeID) {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution result does not match the frozen stage executor contract")}
	}
	if len(contract.ParticipantNodeIDs) > 0 && !reflect.DeepEqual(result.ParticipantNodeIDs, contract.ParticipantNodeIDs) {
		return task.Resource{}, &ValidationError{Err: errors.New("operation execution result participants do not match the frozen canonical participant set")}
	}
	resource, err := service.nodes.CompleteTask(ctx, strings.TrimSpace(agentID), strings.TrimSpace(taskID), submission, service.now().UTC())
	if err != nil {
		return task.Resource{}, classifyTaskConflict(err)
	}
	if err := service.continuation.ContinueOperationRun(ctx, contract.RunID); err != nil {
		return resource, err
	}
	return resource, nil
}
