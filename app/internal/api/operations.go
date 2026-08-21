package api

import (
	"net/http"

	"setpoint/internal/protocol"
)

func (handler *Handler) listOperations(writer http.ResponseWriter, _ *http.Request) {
	definitions, err := handler.operations.ListOperations()
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"operations": definitions})
}

func (handler *Handler) getOperation(writer http.ResponseWriter, request *http.Request) {
	definition, err := handler.operations.GetOperation(request.PathValue("operation_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, definition)
}

func (handler *Handler) createOperationRun(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.CreateOperationRunRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	run, created, err := handler.operations.CreateOperationRun(request.Context(), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, run)
}

func (handler *Handler) listOperationRuns(writer http.ResponseWriter, request *http.Request) {
	options, ok := parseListOptions(writer, request)
	if !ok {
		return
	}
	runs, normalized, err := handler.operations.ListOperationRuns(request.Context(), options)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, protocol.OperationRunListResponse{Runs: runs, Limit: normalized.Limit, Offset: normalized.Offset})
}

func (handler *Handler) getOperationRun(writer http.ResponseWriter, request *http.Request) {
	run, err := handler.operations.GetOperationRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, run)
}

func (handler *Handler) confirmOperationRun(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.ConfirmOperationRunRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	run, err := handler.operations.ConfirmOperationRun(request.Context(), request.PathValue("run_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, run)
}

func (handler *Handler) cancelOperationRun(writer http.ResponseWriter, request *http.Request) {
	run, err := handler.operations.CancelOperationRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, run)
}
