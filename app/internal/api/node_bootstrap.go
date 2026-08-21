package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"setpoint/internal/bootstrap"
	"setpoint/internal/protocol"
)

type nodeBootstrapRouter struct {
	next    http.Handler
	service *bootstrap.Service
	logger  *slog.Logger
}

func WithNodeBootstrap(next http.Handler, service *bootstrap.Service, logger *slog.Logger) (http.Handler, error) {
	if next == nil || service == nil || logger == nil {
		return nil, errors.New("next handler, bootstrap service and logger are required")
	}
	return &nodeBootstrapRouter{next: next, service: service, logger: logger}, nil
}

func (router *nodeBootstrapRouter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	tracked := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
	handled := true
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/node-bootstrap/probe":
		router.probe(tracked, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/node-bootstrap/apply":
		router.apply(tracked, request)
	default:
		handled = false
		router.next.ServeHTTP(writer, request)
	}
	if handled {
		router.logger.Info("HTTP request", "method", request.Method, "path", request.URL.Path,
			"status", tracked.status, "duration_ms", time.Since(started).Milliseconds())
	}
}

func (router *nodeBootstrapRouter) probe(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.NodeBootstrapProbeRequest
	if !decodeBootstrapJSON(writer, request, &payload) {
		return
	}
	probe, err := router.service.Probe(request.Context(), bootstrap.ProbeInput{
		Address: payload.Address, Port: payload.Port, Username: payload.Username, Password: payload.Password,
		Gateway: gatewayInput(payload.Gateway),
	})
	if err != nil {
		writeBootstrapError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, protocol.NodeBootstrapProbeResponse{
		HostKeyFingerprint: probe.HostKeyFingerprint, GatewayHostKeyFingerprint: probe.GatewayHostKeyFingerprint,
		OS: probe.OS, OSVersion: probe.OSVersion, Arch: probe.Arch, Username: probe.Username,
		UID: probe.UID, Mode: probe.InstallProfile.Mode, Home: probe.Home,
		AgentPresent: probe.AgentPresent, TargetInstallProfile: probe.InstallProfile,
	})
}

func (router *nodeBootstrapRouter) apply(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.NodeBootstrapApplyRequest
	if !decodeBootstrapJSON(writer, request, &payload) {
		return
	}
	node, err := router.service.Apply(request.Context(), bootstrap.ApplyInput{
		ProbeInput: bootstrap.ProbeInput{
			Address: payload.Address, Port: payload.Port, Username: payload.Username, Password: payload.Password,
			Gateway: gatewayInput(payload.Gateway),
		},
		ExpectedHostKeyFingerprint:        payload.ExpectedHostKeyFingerprint,
		ExpectedGatewayHostKeyFingerprint: payload.ExpectedGatewayHostKeyFingerprint,
		SiteID:                            payload.SiteID,
	})
	if err != nil {
		writeBootstrapError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, protocol.NodeBootstrapApplyResponse{
		NodeID: node.ID, Hostname: node.Hostname, OS: node.OS, OSVersion: node.OSVersion,
		Arch: node.Arch, AgentVersion: node.AgentVersion, Status: "online", SiteID: payload.SiteID,
	})
}

func gatewayInput(value *protocol.NodeBootstrapGatewayRequest) *bootstrap.GatewayInput {
	if value == nil {
		return nil
	}
	return &bootstrap.GatewayInput{Address: value.Address, Port: value.Port, Username: value.Username, Password: value.Password}
}

func decodeBootstrapJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	if !isJSON(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	if err := decodeJSON(writer, request, target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return false
		}
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return false
	}
	return true
}

func writeBootstrapError(writer http.ResponseWriter, err error) {
	var typed *bootstrap.Error
	if errors.As(err, &typed) {
		status := http.StatusBadRequest
		switch typed.Code {
		case bootstrap.ErrorAlreadyPresent:
			status = http.StatusConflict
		case bootstrap.ErrorGatewayConnectFailed, bootstrap.ErrorGatewayAuthFailed,
			bootstrap.ErrorGatewayTargetUnreachable, bootstrap.ErrorTargetConnectFailed,
			bootstrap.ErrorTargetAuthFailed, bootstrap.ErrorArtifactHashMismatch,
			bootstrap.ErrorAgentRuntimeUnreachable, bootstrap.ErrorHeartbeatTimeout,
			bootstrap.ErrorAgentStartFailed, bootstrap.ErrorEnrollmentFailed:
			status = http.StatusBadGateway
		case bootstrap.ErrorArtifactNotFound:
			status = http.StatusServiceUnavailable
		}
		writeError(writer, status, typed.Code, typed.Error())
		return
	}
	writeError(writer, http.StatusBadGateway, "bootstrap_failed", "Agent bootstrap failed")
}
