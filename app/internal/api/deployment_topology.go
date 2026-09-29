package api

import (
	"context"
	"net/http"

	"setpoint/internal/protocol"
)

type deploymentTopologyService interface {
	CreateDeploymentTopologyDiscovery(context.Context, string, protocol.CreateDeploymentTopologyDiscoveryRequest) (protocol.DeploymentTopologyDiscoveryResource, bool, error)
	GetDeploymentTopologyDiscovery(context.Context, string) (protocol.DeploymentTopologyDiscoveryResource, error)
	GetNodeDeploymentTopology(context.Context, string) (protocol.DeploymentTopologyDiscoveryResource, error)
}

func (handler *Handler) topologyService() deploymentTopologyService {
	service, _ := handler.service.(deploymentTopologyService)
	return service
}

func (handler *Handler) createDeploymentTopologyDiscovery(writer http.ResponseWriter, request *http.Request) {
	service := handler.topologyService()
	if service == nil {
		writeError(writer, http.StatusNotImplemented, "topology_discovery_unavailable", "deployment topology discovery is unavailable")
		return
	}
	if !isJSON(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	var payload protocol.CreateDeploymentTopologyDiscoveryRequest
	if err := decodeJSON(writer, request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}
	resource, created, err := service.CreateDeploymentTopologyDiscovery(request.Context(), request.PathValue("node_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, resource)
}

func (handler *Handler) getDeploymentTopologyDiscovery(writer http.ResponseWriter, request *http.Request) {
	service := handler.topologyService()
	if service == nil {
		writeError(writer, http.StatusNotImplemented, "topology_discovery_unavailable", "deployment topology discovery is unavailable")
		return
	}
	resource, err := service.GetDeploymentTopologyDiscovery(request.Context(), request.PathValue("discovery_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}

func (handler *Handler) getNodeDeploymentTopology(writer http.ResponseWriter, request *http.Request) {
	service := handler.topologyService()
	if service == nil {
		writeError(writer, http.StatusNotImplemented, "topology_discovery_unavailable", "deployment topology discovery is unavailable")
		return
	}
	resource, err := service.GetNodeDeploymentTopology(request.Context(), request.PathValue("node_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}
