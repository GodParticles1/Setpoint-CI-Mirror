package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"setpoint/internal/bootstrap"
)

func TestWriteBootstrapErrorPreservesTypedCodeWithoutLeakingCause(t *testing.T) {
	cases := []struct {
		code   string
		status int
	}{
		{bootstrap.ErrorGatewayConnectFailed, http.StatusBadGateway},
		{bootstrap.ErrorGatewayAuthFailed, http.StatusBadGateway},
		{bootstrap.ErrorGatewayHostKeyChanged, http.StatusBadRequest},
		{bootstrap.ErrorGatewayTargetUnreachable, http.StatusBadGateway},
		{bootstrap.ErrorTargetConnectFailed, http.StatusBadGateway},
		{bootstrap.ErrorTargetAuthFailed, http.StatusBadGateway},
		{bootstrap.ErrorTargetHostKeyChanged, http.StatusBadRequest},
		{bootstrap.ErrorArtifactNotFound, http.StatusServiceUnavailable},
		{bootstrap.ErrorArtifactHashMismatch, http.StatusBadGateway},
		{bootstrap.ErrorAgentRuntimeUnreachable, http.StatusBadGateway},
		{bootstrap.ErrorAgentStartFailed, http.StatusBadGateway},
		{bootstrap.ErrorEnrollmentFailed, http.StatusBadGateway},
		{bootstrap.ErrorHeartbeatTimeout, http.StatusBadGateway},
	}
	for _, testCase := range cases {
		t.Run(testCase.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeBootstrapError(response, &bootstrap.Error{
				Code: testCase.code, Message: "bounded bootstrap failure", Err: errors.New("PASSWORD_ADDRESS_TOKEN_SENTINEL"),
			})
			if response.Code != testCase.status {
				t.Fatalf("status=%d, want %d", response.Code, testCase.status)
			}
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != testCase.code || envelope.Error.Message != "bounded bootstrap failure" {
				t.Fatalf("unexpected envelope: %#v", envelope)
			}
			if strings.Contains(response.Body.String(), "PASSWORD_ADDRESS_TOKEN_SENTINEL") {
				t.Fatal("bootstrap cause leaked through API")
			}
		})
	}
}
