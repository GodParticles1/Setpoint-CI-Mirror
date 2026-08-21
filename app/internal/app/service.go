package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
)

type NodeRepository interface {
	AuthRepository
	ManagementRepository
	TaskRepository
	RegisterNode(context.Context, domain.Registration) (domain.Node, error)
	RecordHeartbeat(context.Context, string, time.Time) error
	ListNodes(context.Context, time.Duration) ([]domain.Node, error)
	GetNode(context.Context, string, time.Duration) (domain.Node, error)
}

type CheckRepository interface {
	UpsertCheck(context.Context, plugin.Metadata) error
}

type CheckCatalog interface {
	List() []plugin.Metadata
	ListDefinitions() []plugin.CheckMetadata
	ListBundles() []plugin.CheckBundle
	ListPolicies() []plugin.CheckPolicy
	ResolveSelection([]string, []string, []string) (plugin.ResolvedCheckSelection, error)
	SupportsCheckExecution(string) bool
	Get(string) (plugin.Metadata, bool)
}

type Service struct {
	nodes        NodeRepository
	checkStore   CheckRepository
	checks       CheckCatalog
	offlineAfter time.Duration
	now          func() time.Time
}

func NewService(nodes NodeRepository, checkStore CheckRepository, checks CheckCatalog, offlineAfter time.Duration) (*Service, error) {
	if nodes == nil || checkStore == nil || checks == nil {
		return nil, errors.New("node store, plugin store and check catalog are required")
	}
	if offlineAfter <= 0 {
		return nil, errors.New("offline timeout must be positive")
	}
	return &Service{
		nodes: nodes, checkStore: checkStore, checks: checks,
		offlineAfter: offlineAfter, now: time.Now,
	}, nil
}

func (service *Service) SyncChecks(ctx context.Context) error {
	for _, metadata := range service.checks.List() {
		if err := service.checkStore.UpsertCheck(ctx, metadata); err != nil {
			return fmt.Errorf("sync plugin %s: %w", metadata.ID, err)
		}
	}
	return nil
}

func (service *Service) Register(ctx context.Context, request protocol.RegistrationRequest) (protocol.RegistrationResponse, error) {
	request = trimRegistration(request)
	if err := validateRegistration(request); err != nil {
		return protocol.RegistrationResponse{}, &ValidationError{Err: err}
	}
	receivedAt := service.now().UTC()
	node, err := service.nodes.RegisterNode(ctx, domain.Registration{
		AgentID: request.AgentID, Hostname: request.Hostname, OS: request.OS,
		OSVersion: request.OSVersion, Arch: request.Arch, AgentVersion: request.AgentVersion,
		ObservedSourceAddress: request.ObservedSourceAddress, ReceivedAt: receivedAt,
	})
	if err != nil {
		return protocol.RegistrationResponse{}, err
	}
	return protocol.RegistrationResponse{NodeID: node.ID, RegisteredAt: receivedAt}, nil
}

func (service *Service) Heartbeat(ctx context.Context, agentID string) (protocol.HeartbeatResponse, error) {
	agentID = strings.TrimSpace(agentID)
	if err := validateIdentifier(agentID); err != nil {
		return protocol.HeartbeatResponse{}, &ValidationError{Err: fmt.Errorf("agent_id: %w", err)}
	}
	receivedAt := service.now().UTC()
	if err := service.nodes.RecordHeartbeat(ctx, agentID, receivedAt); err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	return protocol.HeartbeatResponse{AgentID: agentID, AcknowledgedAt: receivedAt}, nil
}

func (service *Service) ListNodes(ctx context.Context) ([]domain.Node, error) {
	return service.nodes.ListNodes(ctx, service.offlineAfter)
}

func (service *Service) GetNode(ctx context.Context, id string) (domain.Node, error) {
	if err := validateIdentifier(id); err != nil {
		return domain.Node{}, &ValidationError{Err: fmt.Errorf("node id: %w", err)}
	}
	return service.nodes.GetNode(ctx, id, service.offlineAfter)
}

func (service *Service) ListChecks() []plugin.Metadata {
	return service.checks.List()
}

func (service *Service) ListCheckDefinitions() []plugin.CheckMetadata {
	return service.checks.ListDefinitions()
}

func (service *Service) ListCheckBundles() []plugin.CheckBundle {
	return service.checks.ListBundles()
}

func (service *Service) ListCheckPolicies() []plugin.CheckPolicy {
	return service.checks.ListPolicies()
}

func trimRegistration(request protocol.RegistrationRequest) protocol.RegistrationRequest {
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.Hostname = strings.TrimSpace(request.Hostname)
	request.OS = strings.TrimSpace(request.OS)
	request.OSVersion = strings.TrimSpace(request.OSVersion)
	request.Arch = strings.TrimSpace(request.Arch)
	request.AgentVersion = strings.TrimSpace(request.AgentVersion)
	return request
}

func validateRegistration(request protocol.RegistrationRequest) error {
	if err := validateIdentifier(request.AgentID); err != nil {
		return fmt.Errorf("agent_id: %w", err)
	}
	fields := map[string]string{
		"hostname": request.Hostname, "os": request.OS, "os_version": request.OSVersion,
		"arch": request.Arch, "agent_version": request.AgentVersion,
	}
	for name, value := range fields {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
		if len(value) > 255 {
			return fmt.Errorf("%s exceeds 255 bytes", name)
		}
	}
	return nil
}

func validateIdentifier(value string) error {
	if value == "" {
		return errors.New("is required")
	}
	if len(value) > 128 {
		return errors.New("exceeds 128 bytes")
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._:-", character) {
			continue
		}
		return errors.New("contains unsupported characters")
	}
	return nil
}
