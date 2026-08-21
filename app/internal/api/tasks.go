package api

import (
	"errors"
	"net/http"

	"setpoint/internal/protocol"
	"setpoint/internal/task"
)

func (handler *Handler) createTask(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.CreateTaskRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	resource, created, err := handler.service.CreateTask(request.Context(), payload)
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

func (handler *Handler) listTasks(writer http.ResponseWriter, request *http.Request) {
	resources, err := handler.service.ListTasks(request.Context())
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, protocol.TaskListResponse{Tasks: resources})
}

func (handler *Handler) getTask(writer http.ResponseWriter, request *http.Request) {
	resource, err := handler.service.GetTask(request.Context(), request.PathValue("task_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}

func (handler *Handler) cancelTask(writer http.ResponseWriter, request *http.Request) {
	resource, err := handler.service.CancelTask(request.Context(), request.PathValue("task_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}

func (handler *Handler) claimTask(writer http.ResponseWriter, request *http.Request) {
	agentID := request.PathValue("agent_id")
	if err := handler.service.AuthenticateAgent(request.Context(), request.Header.Get("Authorization"), agentID); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	resource, err := handler.service.ClaimTask(request.Context(), agentID)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	if resource == nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(writer, http.StatusOK, protocol.ClaimTaskResponse{Task: *resource})
}

func (handler *Handler) acknowledgeTask(writer http.ResponseWriter, request *http.Request) {
	agentID := request.PathValue("agent_id")
	if err := handler.service.AuthenticateAgent(request.Context(), request.Header.Get("Authorization"), agentID); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	var payload protocol.AcknowledgeTaskRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	resource, err := handler.service.AcknowledgeTask(request.Context(), agentID, request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}

func (handler *Handler) submitTaskResult(writer http.ResponseWriter, request *http.Request) {
	agentID := request.PathValue("agent_id")
	if err := handler.service.AuthenticateAgent(request.Context(), request.Header.Get("Authorization"), agentID); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	var payload task.ResultSubmission
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	resource, err := handler.service.SubmitTaskResult(request.Context(), agentID, request.PathValue("task_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, resource)
}

func taskConflictCode(err error) string {
	switch {
	case errors.Is(err, task.ErrClaimMismatch):
		return "task_claim_mismatch"
	case errors.Is(err, task.ErrNodeMismatch):
		return "task_agent_mismatch"
	case errors.Is(err, task.ErrResultConflict):
		return "task_result_conflict"
	case errors.Is(err, task.ErrIdempotencyConflict):
		return "task_idempotency_conflict"
	default:
		return "task_state_conflict"
	}
}
