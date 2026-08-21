package nginxbaseline

import (
	"context"
	"encoding/json"
	"testing"

	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

func TestLocationAliasBoundaryConclusions(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   task.ItemStatus
	}{
		{"safe trailing boundaries", `http { server { location /files/ { alias /srv/files/; } } }`, task.ItemSafe},
		{"unsafe source example", `http { server { location /foo { alias /bar/; } } }`, task.ItemUnsafe},
		{"regex requires review", `http { server { location ~ ^/files { alias /srv/files/; } } }`, task.ItemManualReview},
		{"include prevents proof", `http { include conf.d/*.conf; server { location /files/ { alias /srv/files/; } } }`, task.ItemManualReview},
		{"no alias is not applicable", `http { server { listen 80; } }`, task.ItemNotApplicable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item, _, err := runSelectedNginx(test.config, "nginx.location_alias_boundary", nil)
			if err != nil || item.Status != test.want {
				t.Fatalf("item=%#v err=%v", item, err)
			}
		})
	}
}

func TestSecurityHeadersRespectInheritanceAndOverrides(t *testing.T) {
	good := `http {
add_header X-Frame-Options SAMEORIGIN always;
add_header X-XSS-Protection "1; mode=block" always;
add_header X-Content-Type-Options nosniff always;
add_header Referrer-Policy no-referrer-when-downgrade always;
server { listen 80; location /nested/ { root /srv/www; } }
server { listen 8080; }
}`
	for _, id := range []string{
		"nginx.header.x_frame_options", "nginx.header.x_xss_protection",
		"nginx.header.x_content_type_options", "nginx.header.referrer_policy",
	} {
		item, _, err := runSelectedNginx(good, id, nil)
		if err != nil || item.Status != task.ItemSafe {
			t.Fatalf("%s item=%#v err=%v", id, item, err)
		}
		item, _, err = runSelectedNginx(`http { server { listen 80; } }`, id, nil)
		if err != nil || item.Status != task.ItemUnsafe {
			t.Fatalf("%s missing item=%#v err=%v", id, item, err)
		}
		item, _, err = runSelectedNginx(`http { include conf.d/*.conf; server { listen 80; } }`, id, nil)
		if err != nil || item.Status != task.ItemManualReview {
			t.Fatalf("%s ambiguous missing item=%#v err=%v", id, item, err)
		}
	}

	override := `http {
add_header X-Frame-Options SAMEORIGIN always;
server { listen 80; location /nested/ { add_header X-Other value always; } }
}`
	item, _, err := runSelectedNginx(override, "nginx.header.x_frame_options", nil)
	if err != nil || item.Status != task.ItemUnsafe {
		t.Fatalf("override item=%#v err=%v", item, err)
	}

	dynamic := `http { add_header X-Frame-Options $frame_policy always; server { listen 80; } }`
	item, _, err = runSelectedNginx(dynamic, "nginx.header.x_frame_options", nil)
	if err != nil || item.Status != task.ItemManualReview {
		t.Fatalf("dynamic item=%#v err=%v", item, err)
	}

	included := `http { include conf.d/*.conf; add_header X-Frame-Options SAMEORIGIN always; server { listen 80; } }`
	item, _, err = runSelectedNginx(included, "nginx.header.x_frame_options", nil)
	if err != nil || item.Status != task.ItemManualReview {
		t.Fatalf("include item=%#v err=%v", item, err)
	}
}

func TestBatch2HTTPChecksIgnoreStreamScope(t *testing.T) {
	mixed := `stream { server { add_header X-Frame-Options BAD; listen 9000; } }
http { add_header X-Frame-Options DENY always; server { listen 80; } }`
	item, _, err := runSelectedNginx(mixed, "nginx.header.x_frame_options", nil)
	if err != nil || item.Status != task.ItemSafe {
		t.Fatalf("mixed item=%#v err=%v", item, err)
	}
	item, _, err = runSelectedNginx(`stream { server { listen 9000; } }`, "nginx.error_page_404", nil)
	if err != nil || item.Status != task.ItemNotApplicable {
		t.Fatalf("stream item=%#v err=%v", item, err)
	}
}

func TestExpandedIncludeWithoutRecoverableScopeRequiresReview(t *testing.T) {
	config := `http { include conf.d/*.conf; }
server { add_header X-Frame-Options SAMEORIGIN always; error_page 404 /404.html; listen 80; }`
	for _, id := range []string{"nginx.header.x_frame_options", "nginx.error_page_404", "nginx.cors.allow_origin"} {
		item, _, err := runSelectedNginx(config, id, nil)
		if err != nil || item.Status != task.ItemManualReview {
			t.Fatalf("%s item=%#v err=%v", id, item, err)
		}
	}
}

func runSelectedNginx(config, id string, parameters json.RawMessage) (task.CheckItem, *nginxFixtureExecutor, error) {
	execution := nginxExecution(config)
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux", SelectedCheckIDs: []string{id}, Parameters: parameters,
	})
	if len(items) != 1 {
		return task.CheckItem{}, execution, err
	}
	return items[0], execution, err
}
