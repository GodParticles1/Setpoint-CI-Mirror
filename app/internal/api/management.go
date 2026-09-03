package api

import (
	"net/http"
	"strconv"

	"setpoint/internal/checkrun"
	"setpoint/internal/plugin"
	"setpoint/internal/protocol"
)

func (handler *Handler) createSite(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.CreateSiteRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	site, created, err := handler.service.CreateSite(request.Context(), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, site)
}

func (handler *Handler) listSites(writer http.ResponseWriter, request *http.Request) {
	sites, err := handler.service.ListSites(request.Context())
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"sites": sites})
}

func (handler *Handler) updateSite(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.UpdateSiteRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	site, err := handler.service.UpdateSite(request.Context(), request.PathValue("site_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, site)
}

func (handler *Handler) deleteSite(writer http.ResponseWriter, request *http.Request) {
	if err := handler.service.DeleteSite(request.Context(), request.PathValue("site_id")); err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) updateNode(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.UpdateNodeRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	node, err := handler.service.UpdateNode(request.Context(), request.PathValue("node_id"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, node)
}

func (handler *Handler) createCheckRun(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.CreateCheckRunRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	run, created, err := handler.service.CreateCheckRun(request.Context(), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(writer, status, decorateCheckRun(run, handler.service.ListCheckDefinitions()))
}

func (handler *Handler) listCheckRuns(writer http.ResponseWriter, request *http.Request) {
	options, ok := parseListOptions(writer, request)
	if !ok {
		return
	}
	runs, normalized, err := handler.service.ListCheckRuns(request.Context(), options)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	definitions := handler.service.ListCheckDefinitions()
	for index := range runs {
		runs[index] = decorateCheckRun(runs[index], definitions)
	}
	writeJSON(writer, http.StatusOK, protocol.CheckRunListResponse{
		Runs: runs, Limit: normalized.Limit, Offset: normalized.Offset,
	})
}

func (handler *Handler) getCheckRun(writer http.ResponseWriter, request *http.Request) {
	run, err := handler.service.GetCheckRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, decorateCheckRun(run, handler.service.ListCheckDefinitions()))
}

func decorateCheckRun(run checkrun.Resource, definitions []plugin.CheckMetadata) checkrun.Resource {
	remediations := make(map[string]plugin.RemediationMetadata, len(definitions))
	for _, definition := range definitions {
		remediations[definition.ID] = definition.Remediation
	}
	run.RemediationOffers = checkrun.BuildRemediationOffers(run, remediations)
	return run
}

func (handler *Handler) cancelCheckRun(writer http.ResponseWriter, request *http.Request) {
	response, err := handler.service.CancelCheckRun(request.Context(), request.PathValue("run_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	response.Run = decorateCheckRun(response.Run, handler.service.ListCheckDefinitions())
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) dashboard(writer http.ResponseWriter, request *http.Request) {
	summary, err := handler.service.Dashboard(request.Context())
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, summary)
}

func (handler *Handler) settings(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, handler.service.Settings())
}

func parseListOptions(writer http.ResponseWriter, request *http.Request) (protocol.ListOptions, bool) {
	options := protocol.ListOptions{}
	for name, target := range map[string]*int{"limit": &options.Limit, "offset": &options.Offset} {
		value := request.URL.Query().Get(name)
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeError(writer, http.StatusBadRequest, "invalid_query", name+" must be a non-negative integer")
			return protocol.ListOptions{}, false
		}
		*target = parsed
	}
	return options, true
}
