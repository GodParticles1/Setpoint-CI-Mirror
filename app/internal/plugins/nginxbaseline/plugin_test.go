package nginxbaseline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

const coveredConfig = `
# configuration file /etc/nginx/nginx.conf:
user nginx;
http {
    server_tokens off;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers "HIGH:!aNULL:!MD5:!3DES";
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options SAMEORIGIN always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy no-referrer-when-downgrade always;
    add_header Access-Control-Allow-Origin https://approved.example always;
    error_page 404 /404.html;
    server {
        listen 443 ssl;
        ssl_certificate /etc/pki/tls/cert.pem;
        location /assets/ { alias /srv/assets/; }
    }
}
`

func TestNginxBaselineUsesConservativeConclusionsForCoveredConfiguration(t *testing.T) {
	items, err := runNginxCheck(t, coveredConfig)
	if err != nil || len(items) != len(definitions) {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	want := []task.ItemStatus{
		task.ItemManualReview,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemManualReview,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemSafe,
		task.ItemManualReview,
	}
	assertStatuses(t, items, want)
	if items[0].ReviewReason == "" || items[5].ReviewReason == "" {
		t.Fatalf("manual review reason missing: %#v", items)
	}
	for _, item := range items {
		if strings.Contains(item.EvidenceSummary, "cert.pem") || strings.Contains(item.EvidenceSummary, "includeSubDomains") {
			t.Fatalf("evidence retained configuration details: %#v", item)
		}
	}
}

func TestNginxBaselineMarksTLSChecksNotApplicableWithoutTLS(t *testing.T) {
	items, err := runNginxCheck(t, `user nginx; http { server_tokens off; server { listen 80; } }`)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{4, 5, 6} {
		if items[index].Status != task.ItemNotApplicable {
			t.Fatalf("item %d=%#v", index, items[index])
		}
	}
	if items[0].Status != task.ItemManualReview {
		t.Fatalf("version item=%#v", items[0])
	}
}

func TestNginxBaselineDoesNotDeclareTruncatedConfigSafe(t *testing.T) {
	execution := nginxExecution(coveredConfig)
	result := execution.results[nginxKey("nginx", "-T")]
	result.StdoutTruncated = true
	execution.results[nginxKey("nginx", "-T")] = result
	items, err := New().Check(context.Background(), plugin.CheckInput{Executor: execution, System: "linux"})
	if err == nil {
		t.Fatal("truncated configuration was accepted")
	}
	for _, item := range items[2:] {
		if item.Status != task.ItemError {
			t.Fatalf("truncated item=%#v", item)
		}
	}
}

func TestNginxBaselineRequiresReviewForPartialHSTSCoverage(t *testing.T) {
	config := `
user nginx;
http {
    server_tokens off;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!3DES;
    server {
        listen 443 ssl;
        ssl_certificate first.pem;
        add_header Strict-Transport-Security "max-age=31536000" always;
    }
    server {
        listen 8443 ssl;
        ssl_certificate second.pem;
    }
}`
	items, err := runNginxCheck(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if items[6].Status != task.ItemManualReview || items[6].ReviewReason == "" {
		t.Fatalf("hsts item=%#v", items[6])
	}
}

func TestNginxBaselineRequiresReviewWhenNestedHeaderReplacesInheritance(t *testing.T) {
	config := `
user nginx;
http {
    server_tokens off;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!3DES;
    add_header Strict-Transport-Security "max-age=31536000" always;
    server {
        listen 443 ssl;
        ssl_certificate server.pem;
        add_header X-Content-Type-Options nosniff always;
    }
}`
	items, err := runNginxCheck(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if items[6].Status != task.ItemManualReview {
		t.Fatalf("hsts item=%#v", items[6])
	}
}

func TestNginxBaselineFindsProtocolDifferencesAcrossTLSContexts(t *testing.T) {
	config := `
user nginx;
http {
    server_tokens off;
    ssl_ciphers HIGH:!3DES;
    add_header Strict-Transport-Security "max-age=31536000" always;
    server { listen 443 ssl; ssl_certificate first.pem; ssl_protocols TLSv1.2 TLSv1.3; }
    server { listen 8443 ssl; ssl_certificate second.pem; ssl_protocols TLSv1 TLSv1.2; }
}`
	items, err := runNginxCheck(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if items[4].Status != task.ItemUnsafe {
		t.Fatalf("protocol item=%#v", items[4])
	}
}

func TestNginxBaselineRejectsInvalidHSTS(t *testing.T) {
	for name, header := range map[string]string{
		"short max age":  `add_header Strict-Transport-Security "max-age=0" always;`,
		"missing always": `add_header Strict-Transport-Security "max-age=31536000";`,
	} {
		t.Run(name, func(t *testing.T) {
			config := `user nginx; http { server_tokens off; ssl_protocols TLSv1.2 TLSv1.3; ssl_ciphers HIGH:!3DES; ` + header + ` server { listen 443 ssl; ssl_certificate server.pem; } }`
			items, err := runNginxCheck(t, config)
			if err != nil {
				t.Fatal(err)
			}
			if items[6].Status != task.ItemUnsafe {
				t.Fatalf("hsts item=%#v", items[6])
			}
		})
	}
}

func TestNginxBaselineNeverMarksUnexpandedCipherExpressionSafe(t *testing.T) {
	config := `user nginx; http { server_tokens off; ssl_protocols TLSv1.2 TLSv1.3; ssl_ciphers HIGH:!3DES; add_header Strict-Transport-Security "max-age=31536000" always; server { listen 443 ssl; ssl_certificate server.pem; } }`
	items, err := runNginxCheck(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if items[5].Status != task.ItemManualReview {
		t.Fatalf("cipher item=%#v", items[5])
	}

	weak := strings.Replace(config, "HIGH:!3DES", "HIGH:3DES", 1)
	items, err = runNginxCheck(t, weak)
	if err != nil {
		t.Fatal(err)
	}
	if items[5].Status != task.ItemUnsafe {
		t.Fatalf("weak cipher item=%#v", items[5])
	}
}

func TestNginxMissingBinaryBecomesStructuredNotApplicable(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := registry.Register(New()); err != nil {
		t.Fatal(err)
	}
	execution := &nginxFixtureExecutor{errors: map[string]error{
		nginxKey("nginx", "-v"): &executor.Error{Kind: executor.ErrorStart, Err: fmt.Errorf("%w: nginx", executor.ErrCommandNotFound)},
	}}
	result, err := plugin.ExecuteCheck(context.Background(), registry, ID, plugin.CheckInput{Executor: execution, System: "linux"})
	if err != nil || result.State != task.CheckCompleted || len(result.Items) != len(definitions) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, item := range result.Items {
		if item.Status != task.ItemNotApplicable || item.Error != nil {
			t.Fatalf("item=%#v", item)
		}
	}
}

func TestNginxParserHandlesCommentsQuotesAndNestedBlocks(t *testing.T) {
	configuration := parseConfig(`http { server { listen 443 ssl; add_header X-Test "a # literal"; } # comment
server_tokens off; }`)
	if !configuration.Complete || len(findDirectives(configuration.Directives, "listen")) != 1 ||
		len(findDirectives(configuration.Directives, "add_header")) != 1 ||
		joinedArgs(findDirectives(configuration.Directives, "server_tokens")) != "off" {
		t.Fatalf("configuration=%#v", configuration)
	}
}

func TestNginxParserPreservesBlockIdentityArgumentsAndCompleteness(t *testing.T) {
	configuration := parseConfig(`http { server { location ^~ /assets/ { alias /srv/assets/; } } }`)
	if !configuration.Complete || len(configuration.Blocks) != 3 {
		t.Fatalf("configuration=%#v", configuration)
	}
	location := configuration.Blocks[2]
	if location.Frame.Name != "location" || strings.Join(location.Frame.Args, " ") != "^~ /assets/" ||
		len(location.Parents) != 2 || location.Parents[0].Name != "http" || location.Parents[1].Name != "server" {
		t.Fatalf("location=%#v", location)
	}
	if malformed := parseConfig(`http { server { listen 80;`); malformed.Complete {
		t.Fatalf("malformed configuration was marked complete: %#v", malformed)
	}
}

func TestNginxBaselineMetadataVersion(t *testing.T) {
	if got := New().Metadata().Version; got != "2.2.0" {
		t.Fatalf("version=%q", got)
	}
}

func TestNginxSelectionUsesOnlySharedConfigObservation(t *testing.T) {
	execution := nginxExecution(coveredConfig)
	execution.errors = map[string]error{
		nginxKey("nginx", "-v"): errors.New("unselected version failure"),
		nginxKey("nginx", "-t"): errors.New("unselected syntax failure"),
	}
	items, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: execution, System: "linux",
		SelectedCheckIDs: []string{"nginx.server_tokens", "nginx.hsts", "nginx.server_tokens"},
	})
	if err != nil || len(items) != 2 || items[0].ID != "nginx.server_tokens" || items[1].ID != "nginx.hsts" {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if len(execution.commands) != 1 || nginxKey(execution.commands[0].Name, execution.commands[0].Args...) != nginxKey("nginx", "-T") {
		t.Fatalf("commands=%#v", execution.commands)
	}
}

func TestNginxExplicitAllSelectionMatchesFullCheck(t *testing.T) {
	full, err := runNginxCheck(t, coveredConfig)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.ID)
	}
	selected, err := New().Check(context.Background(), plugin.CheckInput{
		Executor: nginxExecution(coveredConfig), System: "linux", SelectedCheckIDs: ids,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStatuses(t, selected, []task.ItemStatus{
		task.ItemManualReview, task.ItemSafe, task.ItemSafe, task.ItemSafe,
		task.ItemSafe, task.ItemManualReview, task.ItemSafe, task.ItemSafe,
		task.ItemSafe, task.ItemSafe, task.ItemSafe, task.ItemSafe, task.ItemSafe,
		task.ItemManualReview,
	})
	if len(full) != len(selected) {
		t.Fatalf("item counts differ: %d != %d", len(full), len(selected))
	}
	for index := range full {
		if full[index].ID != selected[index].ID || full[index].Status != selected[index].Status ||
			full[index].CurrentValue != selected[index].CurrentValue {
			t.Fatalf("item %d differs: %#v != %#v", index, full[index], selected[index])
		}
	}
}

func runNginxCheck(t *testing.T, config string) ([]task.CheckItem, error) {
	t.Helper()
	return New().Check(context.Background(), plugin.CheckInput{Executor: nginxExecution(config), System: "linux"})
}

func nginxExecution(config string) *nginxFixtureExecutor {
	return &nginxFixtureExecutor{results: map[string]executor.Result{
		nginxKey("nginx", "-v"): {Stderr: "nginx version: nginx/1.24.0\n"},
		nginxKey("nginx", "-t"): {Stderr: "syntax is ok\n"},
		nginxKey("nginx", "-T"): {Stdout: config},
	}}
}

func assertStatuses(t *testing.T, items []task.CheckItem, want []task.ItemStatus) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for index := range want {
		if items[index].Status != want[index] {
			t.Fatalf("item %d status=%s, want %s: %#v", index, items[index].Status, want[index], items[index])
		}
	}
}

type nginxFixtureExecutor struct {
	results  map[string]executor.Result
	errors   map[string]error
	commands []executor.Command
}

func (execution *nginxFixtureExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	lookup := nginxKey(command.Name, command.Args...)
	if err := execution.errors[lookup]; err != nil {
		return execution.results[lookup], err
	}
	return execution.results[lookup], nil
}

func nginxKey(name string, args ...string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}
