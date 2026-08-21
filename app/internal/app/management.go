package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

const (
	defaultListLimit = 50
	maximumListLimit = 200
	maximumRunTasks  = 100
)

type ManagementRepository interface {
	CreateSite(context.Context, domain.Site, string) (domain.Site, bool, error)
	ListSites(context.Context) ([]domain.Site, error)
	GetSite(context.Context, string) (domain.Site, error)
	UpdateSite(context.Context, domain.Site) (domain.Site, error)
	DeleteSite(context.Context, string) error
	UpdateNode(context.Context, string, domain.NodeUpdate) (domain.Node, error)
	CreateCheckRun(context.Context, checkrun.Resource, []task.Resource) (checkrun.Resource, bool, error)
	GetCheckRun(context.Context, string) (checkrun.Resource, error)
	ListCheckRuns(context.Context, int, int) ([]checkrun.Resource, error)
}

type DashboardSummary struct {
	NodesTotal    int       `json:"nodes_total"`
	NodesOnline   int       `json:"nodes_online"`
	NodesOffline  int       `json:"nodes_offline"`
	RecentRuns    int       `json:"recent_runs"`
	Safe          int       `json:"safe"`
	Unsafe        int       `json:"unsafe"`
	Error         int       `json:"error"`
	ManualReview  int       `json:"manual_review"`
	NotApplicable int       `json:"not_applicable"`
	LastCheckAt   time.Time `json:"last_check_at,omitempty"`
}

type RuntimeSettings struct {
	OfflineAfter       string `json:"offline_after"`
	MinimumRefresh     string `json:"minimum_refresh_interval"`
	RecommendedRefresh string `json:"recommended_refresh_interval"`
	MaximumRunTasks    int    `json:"maximum_run_tasks"`
}

func (service *Service) CreateSite(ctx context.Context, request protocol.CreateSiteRequest) (domain.Site, bool, error) {
	if request.APIVersion != "setpoint.io/v1" || request.Kind != "Site" {
		return domain.Site{}, false, &ValidationError{Err: errors.New("api_version and kind must identify a setpoint.io/v1 Site")}
	}
	key := strings.TrimSpace(request.Metadata.IdempotencyKey)
	if err := validateIdentifier(key); err != nil {
		return domain.Site{}, false, &ValidationError{Err: fmt.Errorf("metadata.idempotency_key: %w", err)}
	}
	name, description, rootPaths, err := validateSiteSpec(request.Spec)
	if err != nil {
		return domain.Site{}, false, &ValidationError{Err: err}
	}
	id, err := task.NewID()
	if err != nil {
		return domain.Site{}, false, err
	}
	now := service.now().UTC()
	roots, err := trustedexec.NewConfiguredRoots(trustedexec.ScopeSite, "site:"+id, rootPaths)
	if err != nil {
		return domain.Site{}, false, &ValidationError{Err: err}
	}
	created, wasCreated, err := service.nodes.CreateSite(ctx, domain.Site{
		ID: id, Name: name, Description: description, TrustedExecutableRoots: roots,
		CreatedAt: now, UpdatedAt: now,
	}, key)
	return created, wasCreated, classifyManagementConflict(err)
}

func (service *Service) ListSites(ctx context.Context) ([]domain.Site, error) {
	return service.nodes.ListSites(ctx)
}

func (service *Service) UpdateSite(ctx context.Context, id string, request protocol.UpdateSiteRequest) (domain.Site, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return domain.Site{}, &ValidationError{Err: fmt.Errorf("site id: %w", err)}
	}
	name, description, rootPaths, err := validateSiteSpec(request.Spec)
	if err != nil {
		return domain.Site{}, &ValidationError{Err: err}
	}
	roots, err := trustedexec.NewConfiguredRoots(trustedexec.ScopeSite, "site:"+id, rootPaths)
	if err != nil {
		return domain.Site{}, &ValidationError{Err: err}
	}
	updated, err := service.nodes.UpdateSite(ctx, domain.Site{
		ID: id, Name: name, Description: description, TrustedExecutableRoots: roots,
		UpdatedAt: service.now().UTC(),
	})
	return updated, classifyManagementConflict(err)
}

func (service *Service) DeleteSite(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return &ValidationError{Err: fmt.Errorf("site id: %w", err)}
	}
	return classifyManagementConflict(service.nodes.DeleteSite(ctx, id))
}

func (service *Service) UpdateNode(ctx context.Context, id string, request protocol.UpdateNodeRequest) (domain.Node, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return domain.Node{}, &ValidationError{Err: fmt.Errorf("node id: %w", err)}
	}
	if request.SiteID == nil && request.Tags == nil && request.Notes == nil && request.TrustedExecutableRoots == nil {
		return domain.Node{}, &ValidationError{Err: errors.New("at least one node metadata field is required")}
	}
	update := domain.NodeUpdate{}
	if request.SiteID != nil {
		value := strings.TrimSpace(*request.SiteID)
		if value != "" {
			if err := validateIdentifier(value); err != nil {
				return domain.Node{}, &ValidationError{Err: fmt.Errorf("site_id: %w", err)}
			}
		}
		update.SiteID = &value
	}
	if request.Tags != nil {
		tags, err := normalizeTags(*request.Tags)
		if err != nil {
			return domain.Node{}, &ValidationError{Err: err}
		}
		update.Tags = &tags
	}
	if request.Notes != nil {
		notes := strings.TrimSpace(*request.Notes)
		if len(notes) > 1000 || strings.ContainsRune(notes, 0) {
			return domain.Node{}, &ValidationError{Err: errors.New("notes must be at most 1000 bytes and contain no NUL")}
		}
		update.Notes = &notes
	}
	if request.TrustedExecutableRoots != nil {
		roots, err := trustedexec.NewConfiguredRoots(
			trustedexec.ScopeNode, "node:"+id, *request.TrustedExecutableRoots,
		)
		if err != nil {
			return domain.Node{}, &ValidationError{Err: err}
		}
		update.TrustedExecutableRoots = &roots
	}
	return service.nodes.UpdateNode(ctx, id, update)
}

func (service *Service) CreateCheckRun(
	ctx context.Context,
	request protocol.CreateCheckRunRequest,
) (checkrun.Resource, bool, error) {
	spec, key, name, err := service.validateCheckRunRequest(ctx, request)
	if err != nil {
		return checkrun.Resource{}, false, &ValidationError{Err: err}
	}
	runID, err := task.NewID()
	if err != nil {
		return checkrun.Resource{}, false, err
	}
	createdAt := service.now().UTC()
	run := checkrun.Resource{
		APIVersion: "setpoint.io/v1", Kind: "ReadOnlyCheckRun",
		Metadata: checkrun.Metadata{ID: runID, IdempotencyKey: key, Name: name, CreatedAt: createdAt},
		Spec:     spec,
	}
	rootsByNode := make(map[string][]trustedexec.Root, len(run.Spec.NodeIDs))
	for _, nodeID := range run.Spec.NodeIDs {
		roots, err := service.frozenTrustedRootsForNode(ctx, nodeID)
		if err != nil {
			return checkrun.Resource{}, false, fmt.Errorf("freeze trusted executable roots for node %q: %w", nodeID, err)
		}
		rootsByNode[nodeID] = roots
	}
	tasks, err := buildCheckRunTasks(service.checks, run, createdAt, rootsByNode)
	if err != nil {
		return checkrun.Resource{}, false, err
	}
	created, wasCreated, err := service.nodes.CreateCheckRun(ctx, run, tasks)
	return created, wasCreated, classifyManagementConflict(err)
}

func (service *Service) GetCheckRun(ctx context.Context, id string) (checkrun.Resource, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return checkrun.Resource{}, &ValidationError{Err: fmt.Errorf("check run id: %w", err)}
	}
	return service.nodes.GetCheckRun(ctx, id)
}

func (service *Service) ListCheckRuns(ctx context.Context, options protocol.ListOptions) ([]checkrun.Resource, protocol.ListOptions, error) {
	options = normalizeListOptions(options)
	runs, err := service.nodes.ListCheckRuns(ctx, options.Limit, options.Offset)
	return runs, options, err
}

func (service *Service) CancelCheckRun(ctx context.Context, id string) (checkrun.CancelResponse, error) {
	run, err := service.GetCheckRun(ctx, id)
	if err != nil {
		return checkrun.CancelResponse{}, err
	}
	report := checkrun.CancelReport{
		TotalTasks: len(run.Tasks),
		Results:    make([]checkrun.CancelTaskResult, 0, len(run.Tasks)),
	}
	for _, resource := range run.Tasks {
		result := checkrun.CancelTaskResult{TaskID: resource.Metadata.ID, Phase: resource.Status.Phase}
		if task.Terminal(resource.Status.Phase) {
			result.Outcome = checkrun.CancelOutcomeAlreadyTerminal
			report.AlreadyTerminalTasks++
			report.Results = append(report.Results, result)
			continue
		}
		updated, cancelErr := service.CancelTask(ctx, resource.Metadata.ID)
		if cancelErr != nil {
			result.Outcome = checkrun.CancelOutcomeFailed
			result.Error = &task.Failure{Code: "task_cancel_failed", Message: "cancellation request could not be recorded"}
			report.FailedTasks++
			report.Results = append(report.Results, result)
			continue
		}
		result.Phase = updated.Status.Phase
		switch {
		case updated.Status.Phase == task.PhaseCanceled:
			result.Outcome = checkrun.CancelOutcomeCanceled
			report.CanceledTasks++
		case updated.Status.Phase == task.PhaseCancelRequested:
			result.Outcome = checkrun.CancelOutcomeRequested
			report.CancelRequestedTasks++
		case task.Terminal(updated.Status.Phase):
			result.Outcome = checkrun.CancelOutcomeAlreadyTerminal
			report.AlreadyTerminalTasks++
		default:
			result.Outcome = checkrun.CancelOutcomeFailed
			result.Error = &task.Failure{Code: "task_cancel_state_invalid", Message: "task returned an unsupported cancellation state"}
			report.FailedTasks++
		}
		report.Results = append(report.Results, result)
	}
	latest, err := service.GetCheckRun(ctx, id)
	if err != nil {
		return checkrun.CancelResponse{Report: report}, err
	}
	return checkrun.CancelResponse{Run: latest, Report: report}, nil
}

func (service *Service) Dashboard(ctx context.Context) (DashboardSummary, error) {
	nodes, err := service.ListNodes(ctx)
	if err != nil {
		return DashboardSummary{}, err
	}
	runs, _, err := service.ListCheckRuns(ctx, protocol.ListOptions{Limit: defaultListLimit})
	if err != nil {
		return DashboardSummary{}, err
	}
	summary := DashboardSummary{NodesTotal: len(nodes), RecentRuns: len(runs)}
	for _, node := range nodes {
		if node.Status == domain.NodeStatusOnline {
			summary.NodesOnline++
		} else {
			summary.NodesOffline++
		}
	}
	for _, run := range runs {
		summary.Safe += run.Status.Counts.Safe
		summary.Unsafe += run.Status.Counts.Unsafe
		summary.ManualReview += run.Status.Counts.ManualReview
		summary.Error += run.Status.Counts.Error
		summary.NotApplicable += run.Status.Counts.NotApplicable
		if run.Metadata.CreatedAt.After(summary.LastCheckAt) {
			summary.LastCheckAt = run.Metadata.CreatedAt
		}
	}
	return summary, nil
}

func (service *Service) Settings() RuntimeSettings {
	return RuntimeSettings{
		OfflineAfter: service.offlineAfter.String(), MinimumRefresh: "2s",
		RecommendedRefresh: "5s", MaximumRunTasks: maximumRunTasks,
	}
}

func (service *Service) validateCheckRunRequest(
	ctx context.Context,
	request protocol.CreateCheckRunRequest,
) (checkrun.Spec, string, string, error) {
	if request.APIVersion != "setpoint.io/v1" || request.Kind != "ReadOnlyCheckRun" {
		return checkrun.Spec{}, "", "", errors.New("api_version and kind must identify a setpoint.io/v1 ReadOnlyCheckRun")
	}
	key := strings.TrimSpace(request.Metadata.IdempotencyKey)
	if err := validateIdentifier(key); err != nil {
		return checkrun.Spec{}, "", "", fmt.Errorf("metadata.idempotency_key: %w", err)
	}
	name := strings.TrimSpace(request.Metadata.Name)
	if len(name) > 200 || strings.ContainsRune(name, 0) {
		return checkrun.Spec{}, "", "", errors.New("metadata.name must be at most 200 bytes and contain no NUL")
	}
	nodeIDs, err := normalizedIdentifiers("spec.node_ids", request.Spec.NodeIDs)
	if err != nil {
		return checkrun.Spec{}, "", "", err
	}
	checkIDs, err := optionalNormalizedIdentifiers("spec.check_ids", request.Spec.CheckIDs)
	if err != nil {
		return checkrun.Spec{}, "", "", err
	}
	bundleIDs, err := optionalNormalizedIdentifiers("spec.bundle_ids", request.Spec.BundleIDs)
	if err != nil {
		return checkrun.Spec{}, "", "", err
	}
	policyIDs, err := optionalNormalizedIdentifiers("spec.policy_ids", request.Spec.PolicyIDs)
	if err != nil {
		return checkrun.Spec{}, "", "", err
	}
	selection, err := service.checks.ResolveSelection(checkIDs, bundleIDs, policyIDs)
	if err != nil {
		return checkrun.Spec{}, "", "", fmt.Errorf("resolve check selection: %w", err)
	}
	if len(nodeIDs)*len(selection.Groups) > maximumRunTasks {
		return checkrun.Spec{}, "", "", fmt.Errorf("check run exceeds %d tasks", maximumRunTasks)
	}
	for _, nodeID := range nodeIDs {
		if _, err := service.nodes.GetNode(ctx, nodeID, service.offlineAfter); err != nil {
			return checkrun.Spec{}, "", "", fmt.Errorf("spec.node_ids contains %q: %w", nodeID, err)
		}
	}
	parameters := make(map[string]json.RawMessage, len(selection.Groups))
	for _, group := range selection.Groups {
		metadata, exists := service.checks.Get(group.PluginID)
		if !exists || metadata.Mode != plugin.ModeReadOnly || !service.checks.SupportsCheckExecution(group.PluginID) {
			return checkrun.Spec{}, "", "", fmt.Errorf("selected check uses unavailable read-only plugin %q", group.PluginID)
		}
		raw := request.Spec.Parameters[group.PluginID]
		values, canonical, err := canonicalParameters(raw)
		if err != nil {
			return checkrun.Spec{}, "", "", fmt.Errorf("spec.parameters.%s: %w", group.PluginID, err)
		}
		if err := validateParameterNames(metadata, values); err != nil {
			return checkrun.Spec{}, "", "", fmt.Errorf("spec.parameters.%s: %w", group.PluginID, err)
		}
		parameters[group.PluginID] = canonical
	}
	for pluginID := range request.Spec.Parameters {
		if _, exists := parameters[pluginID]; !exists {
			return checkrun.Spec{}, "", "", fmt.Errorf("spec.parameters contains unselected plugin %q", pluginID)
		}
	}
	return checkrun.Spec{
		NodeIDs: nodeIDs, CheckIDs: selection.CheckIDs, BundleIDs: bundleIDs, PolicyIDs: policyIDs, Parameters: parameters,
	}, key, name, nil
}

func buildCheckRunTasks(
	catalog CheckCatalog,
	run checkrun.Resource,
	createdAt time.Time,
	rootsByNode map[string][]trustedexec.Root,
) ([]task.Resource, error) {
	selection, err := catalog.ResolveSelection(run.Spec.CheckIDs, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve frozen check groups: %w", err)
	}
	tasks := make([]task.Resource, 0, len(run.Spec.NodeIDs)*len(selection.Groups))
	index := 0
	for _, nodeID := range run.Spec.NodeIDs {
		for _, group := range selection.Groups {
			id, err := task.NewID()
			if err != nil {
				return nil, err
			}
			metadata, exists := catalog.Get(group.PluginID)
			if !exists {
				return nil, fmt.Errorf("check plugin %q disappeared while freezing the run", group.PluginID)
			}
			contract, digest, err := plugin.FreezeExecutionContract(
				metadata, group.CheckIDs, run.Spec.Parameters[group.PluginID], rootsByNode[nodeID])
			if err != nil {
				return nil, fmt.Errorf("freeze checks for %q: %w", group.PluginID, err)
			}
			tasks = append(tasks, task.Resource{
				APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
				Metadata: task.Metadata{
					ID: id, IdempotencyKey: fmt.Sprintf("%s:%03d", run.Metadata.ID, index), CreatedAt: createdAt,
				},
				Spec: task.Spec{
					NodeID: nodeID, PluginID: group.PluginID,
					Parameters: append(json.RawMessage(nil), run.Spec.Parameters[group.PluginID]...),
					Execution:  &contract, ContractDigest: digest,
				},
				Status: task.Status{Phase: task.PhasePending, UpdatedAt: createdAt},
			})
			index++
		}
	}
	return tasks, nil
}

func optionalNormalizedIdentifiers(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}
	return normalizedIdentifiers(field, values)
}

func normalizedIdentifiers(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s must not be empty", field)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if err := validateIdentifier(value); err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s contains duplicate %q", field, value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validateSiteSpec(spec protocol.SiteSpec) (string, string, []string, error) {
	name := strings.TrimSpace(spec.Name)
	description := strings.TrimSpace(spec.Description)
	if name == "" || len(name) > 100 || strings.ContainsRune(name, 0) {
		return "", "", nil, errors.New("spec.name is required, at most 100 bytes, and contains no NUL")
	}
	if len(description) > 1000 || strings.ContainsRune(description, 0) {
		return "", "", nil, errors.New("spec.description must be at most 1000 bytes and contain no NUL")
	}
	roots, err := trustedexec.NormalizeConfiguredPaths(spec.TrustedExecutableRoots)
	if err != nil {
		return "", "", nil, fmt.Errorf("spec.trusted_executable_roots: %w", err)
	}
	return name, description, roots, nil
}

func normalizeTags(values []string) ([]string, error) {
	if len(values) > 20 {
		return nil, errors.New("tags must contain at most 20 values")
	}
	seen := make(map[string]struct{}, len(values))
	tags := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 || strings.ContainsRune(value, 0) {
			return nil, errors.New("each tag must be non-empty, at most 64 bytes, and contain no NUL")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		tags = append(tags, value)
	}
	sort.Strings(tags)
	return tags, nil
}

func normalizeListOptions(options protocol.ListOptions) protocol.ListOptions {
	if options.Limit <= 0 {
		options.Limit = defaultListLimit
	}
	if options.Limit > maximumListLimit {
		options.Limit = maximumListLimit
	}
	if options.Offset < 0 {
		options.Offset = 0
	}
	return options
}

func classifyManagementConflict(err error) error {
	if errors.Is(err, domain.ErrIdempotencyConflict) || errors.Is(err, domain.ErrSiteNameConflict) || errors.Is(err, domain.ErrSiteNotEmpty) {
		return &ConflictError{Err: err}
	}
	return err
}
