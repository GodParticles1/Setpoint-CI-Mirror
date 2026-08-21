package api

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"setpoint/internal/auth"
	"setpoint/internal/protocol"
)

func (handler *Handler) createEnrollmentToken(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.CreateEnrollmentTokenRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.CreateEnrollmentToken(request.Context(), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, response)
}

func (handler *Handler) revokeEnrollmentToken(writer http.ResponseWriter, request *http.Request) {
	response, err := handler.service.RevokeEnrollmentToken(request.Context(), request.PathValue("token_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) enrollAgent(writer http.ResponseWriter, request *http.Request) {
	var payload protocol.EnrollmentRequest
	if !handler.decodeRequiredJSON(writer, request, &payload) {
		return
	}
	response, err := handler.service.EnrollAgent(request.Context(), request.Header.Get("Authorization"), payload)
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, response)
}

func (handler *Handler) rotateAgentCredential(writer http.ResponseWriter, request *http.Request) {
	response, err := handler.service.RotateAgentCredential(
		request.Context(), request.Header.Get("Authorization"), request.PathValue("agent_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, response)
}

func (handler *Handler) revokeAgentCredential(writer http.ResponseWriter, request *http.Request) {
	response, err := handler.service.RevokeAgentCredential(request.Context(), request.PathValue("credential_id"))
	if err != nil {
		handler.handleServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) decodeRequiredJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
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

func ProtectManagement(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !remoteIsLoopback(request.RemoteAddr) {
			writeError(writer, http.StatusForbidden, "local_access_required", "management access requires a loopback connection")
			return
		}
		if !managementHostAllowed(request.Host) {
			writeError(writer, http.StatusForbidden, "management_host_rejected", "management Host must be loopback")
			return
		}
		if hasForwardingHeaders(request.Header) {
			writeError(writer, http.StatusForbidden, "management_proxy_rejected", "proxied management requests are not accepted")
			return
		}
		if requestHasSideEffects(request.Method) && !sameOriginManagementRequest(request) {
			writeError(writer, http.StatusForbidden, "cross_site_request_rejected", "cross-site management request rejected")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func managementHostAllowed(value string) bool {
	host := strings.TrimSpace(value)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	} else {
		host = strings.Trim(host, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func hasForwardingHeaders(header http.Header) bool {
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if strings.TrimSpace(header.Get(name)) != "" {
			return true
		}
	}
	return false
}

func requestHasSideEffects(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func sameOriginManagementRequest(request *http.Request) bool {
	site := strings.ToLower(strings.TrimSpace(request.Header.Get("Sec-Fetch-Site")))
	if site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return strings.EqualFold(parsed.Host, request.Host)
}

func remoteHost(remoteAddress string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		return ""
	}
	address := net.ParseIP(host)
	if address == nil {
		return ""
	}
	return address.String()
}

func remoteIsLoopback(remoteAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func writeAuthenticationError(writer http.ResponseWriter, err *auth.Error) {
	status := http.StatusUnauthorized
	if err.Code == auth.CodeAgentMismatch {
		status = http.StatusForbidden
	}
	writer.Header().Set("WWW-Authenticate", `Bearer realm="setpoint-agent"`)
	writeError(writer, status, string(err.Code), authenticationMessage(err.Code))
}

func authenticationMessage(code auth.ErrorCode) string {
	switch code {
	case auth.CodeMissing:
		return "authentication credential is required"
	case auth.CodeMalformed:
		return "authentication credential is malformed"
	case auth.CodeExpired, auth.CodeEnrollmentTokenExpired:
		return "authentication credential has expired"
	case auth.CodeRevoked, auth.CodeEnrollmentTokenRevoked:
		return "authentication credential has been revoked"
	case auth.CodeEnrollmentTokenExhausted:
		return "enrollment token has no remaining uses"
	case auth.CodeAgentMismatch:
		return "authentication credential is not valid for this Agent"
	default:
		return "authentication credential is invalid"
	}
}
