package nginxbaseline

import (
	"testing"

	"setpoint/internal/task"
)

func TestNginxHTTPChecksIgnoreStreamOnlyConfiguration(t *testing.T) {
	items, err := runNginxCheck(t, `
user nginx;
stream {
    ssl_protocols TLSv1;
    ssl_ciphers 3DES;
    server { listen 9443 ssl; }
}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{3, 4, 5, 6} {
		if items[index].Status != task.ItemNotApplicable {
			t.Fatalf("HTTP item %d used stream evidence: %#v", index, items[index])
		}
	}
}

func TestNginxHTTPChecksIgnoreInsecureStreamValuesInMixedConfiguration(t *testing.T) {
	items, err := runNginxCheck(t, `
user nginx;
stream {
    ssl_protocols TLSv1;
    ssl_ciphers 3DES;
    server { listen 9443 ssl; }
}
http {
    server_tokens off;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!3DES;
    add_header Strict-Transport-Security "max-age=31536000" always;
    server { listen 443 ssl; }
}`)
	if err != nil {
		t.Fatal(err)
	}
	if items[3].Status != task.ItemSafe || items[4].Status != task.ItemSafe ||
		items[5].Status != task.ItemManualReview || items[6].Status != task.ItemSafe {
		t.Fatalf("mixed HTTP conclusions=%#v", items)
	}
}

func TestNginxStreamTLSDoesNotMakePlainHTTPApplicableToTLSChecks(t *testing.T) {
	items, err := runNginxCheck(t, `
user nginx;
stream { server { listen 9443 ssl; ssl_protocols TLSv1; } }
http { server_tokens off; server { listen 80; } }
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{4, 5, 6} {
		if items[index].Status != task.ItemNotApplicable {
			t.Fatalf("TLS item %d used stream evidence: %#v", index, items[index])
		}
	}
}
