package nginxbaseline

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"setpoint/internal/task"
)

func TestErrorPage404ConclusionsDoNotClaimReachability(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   task.ItemStatus
	}{
		{"inherited static declaration", `http { error_page 404 /404.html; server { listen 80; location / { root /srv/www; } } }`, task.ItemSafe},
		{"missing declaration", `http { server { listen 80; } }`, task.ItemUnsafe},
		{"dynamic declaration", `http { error_page 404 $error_uri; server { listen 80; } }`, task.ItemManualReview},
		{"include ambiguity", `http { include conf.d/*.conf; error_page 404 /404.html; server { listen 80; } }`, task.ItemManualReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item, _, err := runSelectedNginx(test.config, "nginx.error_page_404", nil)
			if err != nil || item.Status != test.want || strings.Contains(strings.ToLower(item.EvidenceSummary), "reachable") {
				t.Fatalf("item=%#v err=%v", item, err)
			}
		})
	}
}

func TestCORSPolicyConclusionsAndRedaction(t *testing.T) {
	const approved = "https://approved.example"
	policy := json.RawMessage(`{"cors_allowed_origins":"https://approved.example"}`)
	tests := []struct {
		name   string
		value  string
		params json.RawMessage
		want   task.ItemStatus
	}{
		{"exact policy", approved, policy, task.ItemSafe},
		{"wildcard", "*", policy, task.ItemUnsafe},
		{"unapproved", "https://other.example", policy, task.ItemUnsafe},
		{"dynamic", "$http_origin", policy, task.ItemManualReview},
		{"policy omitted", approved, nil, task.ItemManualReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := `http { add_header Access-Control-Allow-Origin ` + test.value + ` always; server { listen 80; } }`
			item, execution, err := runSelectedNginx(config, "nginx.cors.allow_origin", test.params)
			if err != nil || item.Status != test.want {
				t.Fatalf("item=%#v err=%v", item, err)
			}
			rawOriginExposed := len(test.value) > 1 && strings.Contains(fmt.Sprintf("%#v", item), test.value)
			if rawOriginExposed || len(execution.commands) != 1 {
				t.Fatalf("raw origin or unexpected commands: item=%#v commands=%#v", item, execution.commands)
			}
		})
	}
}

func TestCORSPolicyMissingApprovedOriginRequiresReview(t *testing.T) {
	parameters := json.RawMessage(`{"cors_allowed_origins":"https://one.example,https://two.example"}`)
	config := `http { add_header Access-Control-Allow-Origin https://one.example always; server { listen 80; } }`
	item, _, err := runSelectedNginx(config, "nginx.cors.allow_origin", parameters)
	if err != nil || item.Status != task.ItemManualReview {
		t.Fatalf("item=%#v err=%v", item, err)
	}
}

func TestCORSPolicyRejectsInvalidParametersBeforeObservation(t *testing.T) {
	for _, parameters := range []json.RawMessage{
		json.RawMessage(`{"cors_allowed_origins":"*"}`),
		json.RawMessage(`{"cors_allowed_origins":"https://approved.example/path"}`),
		json.RawMessage(`{"cors_allowed_origins":"https://approved.example,https://approved.example"}`),
		json.RawMessage(`{"unknown":"value"}`),
	} {
		item, execution, err := runSelectedNginx(`http { server { listen 80; } }`, "nginx.cors.allow_origin", parameters)
		if err == nil || item.Status != task.ItemError || len(execution.commands) != 0 {
			t.Fatalf("item=%#v err=%v commands=%#v", item, err, execution.commands)
		}
	}
}

func TestUnselectedCORSPolicyDoesNotContaminateOtherCheck(t *testing.T) {
	config := `http { error_page 404 /404.html; server { listen 80; } }`
	item, execution, err := runSelectedNginx(config, "nginx.error_page_404", json.RawMessage(`{"cors_allowed_origins":"*"}`))
	if err != nil || item.Status != task.ItemSafe || len(execution.commands) != 1 {
		t.Fatalf("item=%#v err=%v commands=%#v", item, err, execution.commands)
	}
}

func TestBatch2SourceReferencesAreExact(t *testing.T) {
	want := map[string]string{
		"nginx.location_alias_boundary":       "security-baseline:2.2",
		"nginx.header.x_frame_options":        "security-baseline:2.3",
		"nginx.header.x_xss_protection":       "security-baseline:2.3",
		"nginx.header.x_content_type_options": "security-baseline:2.3",
		"nginx.header.referrer_policy":        "security-baseline:2.3",
		"nginx.error_page_404":                "security-baseline:2.5",
		"nginx.cors.allow_origin":             "security-baseline:2.6",
	}
	for id, expected := range want {
		refs := sourceRefs(id)
		if len(refs) != 1 || refs[0] != expected {
			t.Fatalf("%s source refs=%#v", id, refs)
		}
	}
}
