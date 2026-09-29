package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"setpoint/internal/deploymenttopology"
	"setpoint/internal/domain"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

func (service *Service) CreateDeploymentTopologyDiscovery(ctx context.Context, nodeID string, request protocol.CreateDeploymentTopologyDiscoveryRequest) (protocol.DeploymentTopologyDiscoveryResource, bool, error) {
	nodeID = strings.TrimSpace(nodeID)
	if request.APIVersion != "setpoint.io/v1" {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, &ValidationError{Err: errors.New("api_version must be setpoint.io/v1")}
	}
	if request.Kind != protocol.DeploymentTopologyDiscoveryKind {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, &ValidationError{Err: fmt.Errorf("kind must be %s", protocol.DeploymentTopologyDiscoveryKind)}
	}
	if err := validateIdentifier(nodeID); err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, &ValidationError{Err: fmt.Errorf("node_id: %w", err)}
	}
	key := strings.TrimSpace(request.Metadata.IdempotencyKey)
	if err := validateIdentifier(key); err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, &ValidationError{Err: fmt.Errorf("metadata.idempotency_key: %w", err)}
	}
	if _, err := service.nodes.GetNode(ctx, nodeID, service.offlineAfter); err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, err
	}
	id, err := task.NewID()
	if err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, err
	}
	now := service.now().UTC()
	resource := task.Resource{
		APIVersion: "setpoint.io/v1",
		Kind:       task.KindDeploymentTopologyDiscoveryTask,
		Metadata:   task.Metadata{ID: id, IdempotencyKey: key, CreatedAt: now},
		Spec:       task.Spec{NodeID: nodeID, Parameters: json.RawMessage(`{}`)},
		Status:     task.Status{Phase: task.PhasePending, UpdatedAt: now},
	}
	created, wasCreated, err := service.nodes.CreateTask(ctx, resource)
	if errors.Is(err, task.ErrIdempotencyConflict) {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, &ConflictError{Err: err}
	}
	if err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, false, err
	}
	return deploymentTopologyResource(created), wasCreated, nil
}

func (service *Service) GetDeploymentTopologyDiscovery(ctx context.Context, id string) (protocol.DeploymentTopologyDiscoveryResource, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentifier(id); err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, &ValidationError{Err: fmt.Errorf("discovery id: %w", err)}
	}
	resource, err := service.nodes.GetTask(ctx, id)
	if err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, err
	}
	if resource.Kind != task.KindDeploymentTopologyDiscoveryTask {
		return protocol.DeploymentTopologyDiscoveryResource{}, domain.ErrNotFound
	}
	return deploymentTopologyResource(resource), nil
}

func (service *Service) GetNodeDeploymentTopology(ctx context.Context, nodeID string) (protocol.DeploymentTopologyDiscoveryResource, error) {
	nodeID = strings.TrimSpace(nodeID)
	if err := validateIdentifier(nodeID); err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, &ValidationError{Err: fmt.Errorf("node_id: %w", err)}
	}
	if _, err := service.nodes.GetNode(ctx, nodeID, service.offlineAfter); err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, err
	}
	resources, err := service.nodes.ListTasks(ctx)
	if err != nil {
		return protocol.DeploymentTopologyDiscoveryResource{}, err
	}
	matches := make([]task.Resource, 0)
	for _, resource := range resources {
		if resource.Kind == task.KindDeploymentTopologyDiscoveryTask && resource.Spec.NodeID == nodeID {
			matches = append(matches, resource)
		}
	}
	if len(matches) == 0 {
		return protocol.DeploymentTopologyDiscoveryResource{}, domain.ErrNotFound
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Metadata.CreatedAt.Equal(matches[j].Metadata.CreatedAt) {
			return matches[i].Metadata.ID > matches[j].Metadata.ID
		}
		return matches[i].Metadata.CreatedAt.After(matches[j].Metadata.CreatedAt)
	})
	return deploymentTopologyResource(matches[0]), nil
}

func deploymentTopologyResource(resource task.Resource) protocol.DeploymentTopologyDiscoveryResource {
	result := protocol.DeploymentTopologyDiscoveryResource{
		APIVersion:  "setpoint.io/v1",
		Kind:        protocol.DeploymentTopologyDiscoveryKind,
		ID:          resource.Metadata.ID,
		NodeID:      resource.Spec.NodeID,
		Phase:       resource.Status.Phase,
		CreatedAt:   resource.Metadata.CreatedAt,
		UpdatedAt:   resource.Status.UpdatedAt,
		CompletedAt: resource.Status.CompletedAt,
	}
	if resource.DeploymentTopologyResult != nil {
		topology := resource.DeploymentTopologyResult.Topology
		result.Topology = &topology
		if resource.DeploymentTopologyResult.Error != nil {
			failure := *resource.DeploymentTopologyResult.Error
			result.Error = &failure
		}
	}
	return result
}

func validateDeploymentTopologyTaskResult(resource task.Resource, submission *task.ResultSubmission) error {
	if submission.DeploymentTopologyResult == nil || submission.Result != nil || submission.OperationResult != nil || submission.OperationExecutionResult != nil {
		return errors.New("deployment topology task requires exactly one deployment topology result")
	}
	if !task.ValidResultPhase(submission.Phase) {
		return errors.New("phase must be succeeded, failed, or canceled")
	}
	if resource.Status.Phase == task.PhaseCancelRequested && submission.Phase != task.PhaseCanceled {
		return errors.New("cancel-requested deployment topology task only accepts a canceled result")
	}
	result := submission.DeploymentTopologyResult
	if result.StartedAt.IsZero() || result.CompletedAt.IsZero() || result.CompletedAt.Before(result.StartedAt) {
		return errors.New("deployment topology result has invalid execution timestamps")
	}
	resolved, err := deploymenttopology.Resolve(result.Topology.Evidence)
	if err != nil {
		return fmt.Errorf("deployment topology evidence: %w", err)
	}
	resolved = deploymenttopology.WithParticipantTrust(resolved)
	if !reflect.DeepEqual(resolved, result.Topology) {
		return errors.New("deployment topology result does not match deterministic resolution of submitted evidence")
	}
	switch submission.Phase {
	case task.PhaseSucceeded:
		if result.Error != nil {
			return errors.New("succeeded deployment topology task must not contain an error")
		}
	case task.PhaseFailed, task.PhaseCanceled:
		if result.Error == nil {
			return errors.New("failed or canceled deployment topology task requires an error")
		}
	}
	return nil
}
