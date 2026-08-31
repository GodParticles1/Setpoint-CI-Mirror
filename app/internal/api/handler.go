package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"setpoint/internal/app"
	"setpoint/internal/auth"
	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/operationrun"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

const maxRequestBody = 1 << 20

type HealthStore interface {
	Ping(context.Context) error
}

type Service interface {
	CreateSite(context.Context, protocol.CreateSiteRequest) (domain.Site, bool, error)
	ListSites(context.Context) ([]domain.Site, error)
	UpdateSite(context.Context, string, protocol.UpdateSiteRequest) (domain.Site, error)
	DeleteSite(context.Context, string) error
	UpdateNode(context.Context, string, protocol.UpdateNodeRequest) (domain.Node, error)
	DeleteNode(context.Context, string) error
	CreateCheckRun(context.Context, protocol.CreateCheckRunRequest) (checkrun.Resource, bool, error)
	ListCheckRuns(context.Context, protocol.ListOptions) ([]checkrun.Resource, protocol.ListOptions, error)
	GetCheckRun(context.Context, string) (checkrun.Resource, error)
	CancelCheckRun(context.Context, string) (checkrun.CancelResponse, error)
	Dashboard(context.Context) (app.DashboardSummary, error)
	Settings() app.RuntimeSettings
	CreateEnrollmentToken(context.Context, protocol.CreateEnrollmentTokenRequest) (protocol.EnrollmentTokenResponse, error)
	RevokeEnrollmentToken(context.Context, string) (protocol.RevocationResponse, error)
	EnrollAgent(context.Context, string, protocol.EnrollmentRequest) (protocol.AgentCredentialResponse, error)
	AuthenticateAgent(context.Context, string, string) error
	RotateAgentCredential(context.Context, string, string) (protocol.AgentCredentialResponse, error)
	CreateTask(context.Context, protocol.CreateTaskRequest) (task.Resource, bool, error)
	ListTasks(context.Context) ([]task.Resource, error)
	GetTask(context.Context, string) (task.Resource, error)
	CancelTask(context.Context, string) (task.Resource, error)
	ClaimTask(context.Context, string) (*task.Resource, error)
	AcknowledgeTask(context.Context, string, string, protocol.AcknowledgeTaskRequest) (task.Resource, error)
	SubmitTaskResult(context.Context, string, string, task.ResultSubmission) (task.Resource, error)
	ValidateOperationLease(context.Context, string, string, protocol.OperationLeaseValidationRequest) (protocol.OperationLeaseValidationResponse, error)
	PutOperationLedger(context.Context, string, string, protocol.OperationLedgerPutRequest) error
	GetOperationLedger(context.Context, string, string, protocol.OperationLedgerGetRequest) (protocol.OperationLedgerGetResponse, error)
	ListOperationLedger(context.Context, string, string, protocol.OperationLedgerListRunRequest) (protocol.OperationLedgerListRunResponse, error)
	PutOperationRestore(context.Context, string, string, protocol.OperationRestorePutRequest) error
	GetOperationRestore(context.Context, string, string, protocol.OperationRestoreGetRequest) (protocol.OperationRestoreGetResponse, error)
	ListOperationRestores(context.Context, string, string, protocol.OperationRestoreListRunRequest) (protocol.OperationRestoreListRunResponse, error)
	RevokeAgentCredential(context.Context, string) (protocol.RevocationResponse, error)
	Register(context.Context, protocol.RegistrationRequest) (protocol.RegistrationResponse, error)
	Heartbeat(context.Context, string) (protocol.HeartbeatResponse, error)
	ListNodes(context.Context) ([]domain.Node, error)
	GetNode(context.Context, string) (domain.Node, error)
	ListChecks() []plugin.Metadata
	ListCheckDefinitions() []plugin.CheckMetadata
	ListCheckBundles() []plugin.CheckBundle
	ListCheckPolicies() []plugin.CheckPolicy
}

type OperationsService interface {
	ListOperations() ([]operationrun.DefinitionResource, error)
	GetOperation(string) (operationrun.DefinitionResource, error)
	CreateOperationRun(context.Context, protocol.CreateOperationRunRequest) (operationrun.Resource, bool, error)
	ListOperationRuns(context.Context, protocol.ListOptions) ([]operationrun.Resource, protocol.ListOptions, error)
	GetOperationRun(context.Context, string) (operationrun.Resource, error)
	ConfirmOperationRun(context.Context, string, protocol.ConfirmOperationRunRequest) (operationrun.Resource, error)
	CancelOperationRun(context.Context, string) (operationrun.Resource, error)
}

type Handler struct {
	health     HealthStore
	service    Service
	operations OperationsService
	logger     *slog.Logger
}

func NewManagementHandler(health HealthStore, service Service, logger *slog.Logger) (http.Handler, error) {
	return newManagementHandler(health, service, nil, logger)
}

func NewManagementHandlerWithOperations(health HealthStore, service Service, operations OperationsService, logger *slog.Logger) (http.Handler, error) {
	if operations == nil {
		return nil, errors.New("operations service is required")
	}
	return newManagementHandler(health, service, operations, logger)
}

func newManagementHandler(health HealthStore, service Service, operations OperationsService, logger *slog.Logger) (http.Handler, error) {
	if health == nil || service == nil || logger == nil {
		return nil, errors.New("health store, service and logger are required")
	}
	handler := &Handler{health: health, service: service, operations: operations, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.healthz)
	mux.HandleFunc("POST /api/v1/enrollment-tokens", handler.createEnrollmentToken)
	mux.HandleFunc("POST /api/v1/enrollment-tokens/{token_id}/revoke", handler.revokeEnrollmentToken)
	mux.HandleFunc("POST /api/v1/agent-credentials/{credential_id}/revoke", handler.revokeAgentCredential)
	mux.HandleFunc("POST /api/v1/sites", handler.createSite)
	mux.HandleFunc("GET /api/v1/sites", handler.listSites)
	mux.HandleFunc("PUT /api/v1/sites/{site_id}", handler.updateSite)
	mux.HandleFunc("DELETE /api/v1/sites/{site_id}", handler.deleteSite)
	mux.HandleFunc("GET /api/v1/dashboard/summary", handler.dashboard)
	mux.HandleFunc("GET /api/v1/settings", handler.settings)
	mux.HandleFunc("GET /api/v1/nodes", handler.listNodes)
	mux.HandleFunc("GET /api/v1/nodes/{node_id}", handler.getNode)
	mux.HandleFunc("PATCH /api/v1/nodes/{node_id}", handler.updateNode)
	mux.HandleFunc("DELETE /api/v1/nodes/{node_id}", handler.deleteNode)
	mux.HandleFunc("GET /api/v1/checks", handler.listChecks)
	mux.HandleFunc("GET /api/v1/check-definitions", handler.listCheckDefinitions)
	mux.HandleFunc("GET /api/v1/check-bundles", handler.listCheckBundles)
	mux.HandleFunc("GET /api/v1/check-policies", handler.listCheckPolicies)
	mux.HandleFunc("POST /api/v1/check-runs", handler.createCheckRun)
	mux.HandleFunc("GET /api/v1/check-runs", handler.listCheckRuns)
	mux.HandleFunc("GET /api/v1/check-runs/{run_id}", handler.getCheckRun)
	mux.HandleFunc("POST /api/v1/check-runs/{run_id}/cancel", handler.cancelCheckRun)
	mux.HandleFunc("POST /api/v1/tasks", handler.createTask)
	mux.HandleFunc("GET /api/v1/tasks", handler.listTasks)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}", handler.getTask)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/cancel", handler.cancelTask)
	if operations != nil {
		mux.HandleFunc("GET /api/v1/operations", handler.listOperations)
		mux.HandleFunc("GET /api/v1/operations/{operation_id}", handler.getOperation)
		mux.HandleFunc("POST /api/v1/operation-runs", handler.createOperationRun)
		mux.HandleFunc("GET /api/v1/operation-runs", handler.listOperationRuns)
		mux.HandleFunc("GET /api/v1/operation-runs/{run_id}", handler.getOperationRun)
		mux.HandleFunc("POST /api/v1/operation-runs/{run_id}/confirm", handler.confirmOperationRun)
		mux.HandleFunc("POST /api/v1/operation-runs/{run_id}/cancel", handler.cancelOperationRun)
	}
	return handler.accessLog(ProtectManagement(mux)), nil
}

func NewAgentHandler(service Service, logger *slog.Logger) (http.Handler, error) {
	if service == nil || logger == nil {
		return nil, errors.New("service and logger are required")
	}
	handler := &Handler{service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+protocol.AgentRuntimeReadyPath, handler.agentRuntimeReady)
	mux.HandleFunc("POST /api/v1/agents/enroll", handler.enrollAgent)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/register", handler.register)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/heartbeat", handler.heartbeat)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/credentials/rotate", handler.rotateAgentCredential)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/claim", handler.claimTask)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/ack", handler.acknowledgeTask)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/result", handler.submitTaskResult)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/lease/validate", handler.validateOperationLease)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/ledger/put", handler.putOperationLedger)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/ledger/get", handler.getOperationLedger)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/ledger/list-run", handler.listOperationLedger)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/restore/put", handler.putOperationRestore)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/restore/get", handler.getOperationRestore)
	mux.HandleFunc("POST /api/v1/agents/{agent_id}/tasks/{task_id}/operation-authority/restore/list-run", handler.listOperationRestores)
	return handler.accessLog(mux), nil
}

func (handler *Handler) validateOperationLease(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationLeaseValidationRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.ValidateOperationLease(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) putOperationLedger(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationLedgerPutRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	if err := handler.service.PutOperationLedger(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) getOperationLedger(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationLedgerGetRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.GetOperationLedger(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) listOperationLedger(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationLedgerListRunRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.ListOperationLedger(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) putOperationRestore(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationRestorePutRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	if err := handler.service.PutOperationRestore(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) getOperationRestore(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationRestoreGetRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.GetOperationRestore(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) listOperationRestores(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.OperationRestoreListRunRequest
	if !handler.decodeAuthenticatedAgentJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.ListOperationRestores(request.Context(), request.PathValue("agent_id"), request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) decodeAuthenticatedAgentJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	if !isJSON(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return false
	}
	if err := decodeJSON(writer, request, target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return false
	}
	if err := handler.service.AuthenticateAgent(request.Context(), request.Header.Get("Authorization"), request.PathValue("agent_id")); err != nil {
		handler.handleServiceError(writer, err)
		return false
	}
	return true
}

func (handler *Handler) agentRuntimeReady(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, protocol.AgentRuntimeReadyResponse{
		Status: "ok", Service: protocol.AgentRuntimeReadyService, ContractVersion: protocol.AgentRuntimeContractVersion,
	})
}

func (handler *Handler) healthz(writer http.ResponseWriter, request *http.Request) {
	if err := handler.health.Ping(request.Context()); err != nil {
		handler.logger.Error("health check failed", "error", err)
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) register(writer http.ResponseWriter, request *http.Request) {
	if !isJSON(request.Header.Get("Content-Type")) {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	var payload protocol.RegistrationRequest
	if err := decodeJSON(writer, request, &payload); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
		return
	}
	payload.ObservedSourceAddress = remoteHost(request.RemoteAddr)
	agentID := request.PathValue("agent_id")
	if payload.AgentID != agentID {
		writeError(writer, http.StatusBadRequest, "invalid_request", "agent_id must match the request path")
		return
	}
	if err := handler.service.AuthenticateAgent(request.Context(), request.Header.Get("Authorization"), agentID); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	response, err := handler.service.Register(request.Context(), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, response)
}

func (handler *Handler) heartbeat(writer http.ResponseWriter, request *http.Request) {
	if err := handler.service.AuthenticateAgent(request.Context(), request.Header.Get("Authorization"), request.PathValue("agent_id")); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	response, err := handler.service.Heartbeat(request.Context(), request.PathValue("agent_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) listNodes(writer http.ResponseWriter, request *http.Request) {
	nodes, err := handler.service.ListNodes(request.Context())
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"nodes": nodes})
}

func (handler *Handler) getNode(writer http.ResponseWriter, request *http.Request) {
	node, err := handler.service.GetNode(request.Context(), request.PathValue("node_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, node)
}

func (handler *Handler) deleteNode(writer http.ResponseWriter, request *http.Request) {
	if err := handler.service.DeleteNode(request.Context(), request.PathValue("node_id")); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) listChecks(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"checks": handler.service.ListChecks()})
}

func (handler *Handler) listCheckDefinitions(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"definitions": handler.service.ListCheckDefinitions()})
}

func (handler *Handler) listCheckBundles(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"bundles": handler.service.ListCheckBundles()})
}

func (handler *Handler) listCheckPolicies(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"policies": handler.service.ListCheckPolicies()})
}

func (handler *Handler) handleServiceError(writer http.ResponseWriter, err error) {
	var authError *auth.Error
	if errors.As(err, &authError) {
		writeAuthenticationError(writer, authError)
		return
	}
	if app.IsConflictError(err) {
		writeError(writer, http.StatusConflict, serviceConflictCode(err), err.Error())
		return
	}
	switch {
	case app.IsValidationError(err):
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "resource not found")
	default:
		handler.logger.Error("request failed", "error", err)
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func serviceConflictCode(err error) string {
	switch {
	case errors.Is(err, app.ErrOperationRunIdempotencyConflict):
		return "operation_run_idempotency_conflict"
	case errors.Is(err, app.ErrOperationPlanDigestConflict):
		return "operation_plan_digest_conflict"
	case errors.Is(err, app.ErrProductApplyDisabled):
		return "product_apply_disabled"
	case errors.Is(err, app.ErrOperationExecutionUnavailable):
		return app.OperationExecutionUnavailableBlock
	case errors.Is(err, app.ErrSecretDeliveryUnavailable):
		return app.SecretDeliveryUnavailableBlock
	case errors.Is(err, app.ErrOperationStateConflict):
		return "operation_state_conflict"
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return "resource_idempotency_conflict"
	case errors.Is(err, domain.ErrSiteNameConflict):
		return "site_name_conflict"
	case errors.Is(err, domain.ErrSiteNotEmpty):
		return "site_not_empty"
	case errors.Is(err, domain.ErrNodeActiveWork):
		return "active_work"
	default:
		return taskConflictCode(err)
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Default().Error("encode HTTP response", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (handler *Handler) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		tracked := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(tracked, request)
		handler.logger.Info("HTTP request",
			"method", request.Method, "path", request.URL.Path,
			"status", tracked.status, "duration_ms", time.Since(started).Milliseconds())
	})
}
