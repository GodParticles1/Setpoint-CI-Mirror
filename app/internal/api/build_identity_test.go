package api

import (
	"encoding/json"
	"net/http"
	"setpoint/internal/buildinfo"
	"testing"
)

func TestManagementSettingsReportsBuildIdentity(t *testing.T) {
	response := managementRequest(t, newTestHandler(t), http.MethodGet, "/api/v1/settings", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var settings struct {
		BuildIdentity buildinfo.Identity `json:"build_identity"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	t.Logf("management build_identity=%+v", settings.BuildIdentity)
	if settings.BuildIdentity != buildinfo.Current() {
		t.Fatalf("identity=%+v want=%+v", settings.BuildIdentity, buildinfo.Current())
	}
}
