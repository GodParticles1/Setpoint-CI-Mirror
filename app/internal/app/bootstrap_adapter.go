package app

import (
	"context"
	"errors"
	"time"

	"setpoint/internal/bootstrap"
	"setpoint/internal/domain"
	"setpoint/internal/protocol"
)

func (service *Service) IssueBootstrapEnrollment(ctx context.Context) (bootstrap.EnrollmentToken, error) {
	response, err := service.CreateEnrollmentToken(ctx, protocol.CreateEnrollmentTokenRequest{
		APIVersion: "setpoint.io/v1",
		Kind:       "EnrollmentToken",
		Spec:       protocol.CreateEnrollmentTokenSpec{ExpiresIn: "5m", MaxUses: 1},
	})
	if err != nil {
		return bootstrap.EnrollmentToken{}, err
	}
	return bootstrap.EnrollmentToken{ID: response.Metadata.ID, Secret: response.Secret}, nil
}

func (service *Service) RevokeBootstrapEnrollment(ctx context.Context, id string) error {
	_, err := service.RevokeEnrollmentToken(ctx, id)
	return err
}

func (service *Service) WaitOnline(ctx context.Context, nodeID string, startedAt time.Time) (bootstrap.OnlineNode, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		node, err := service.GetNode(ctx, nodeID)
		if err == nil && node.Status == domain.NodeStatusOnline && !node.LastSeenAt.Before(startedAt) {
			return bootstrap.OnlineNode{
				ID: node.ID, Hostname: node.Hostname, OS: node.OS, OSVersion: node.OSVersion,
				Arch: node.Arch, AgentVersion: node.AgentVersion, LastSeenAt: node.LastSeenAt, Online: true,
			}, nil
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return bootstrap.OnlineNode{}, err
		}
		select {
		case <-ctx.Done():
			return bootstrap.OnlineNode{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (service *Service) AssignSite(ctx context.Context, nodeID, siteID string) error {
	_, err := service.UpdateNode(ctx, nodeID, protocol.UpdateNodeRequest{SiteID: &siteID})
	return err
}
