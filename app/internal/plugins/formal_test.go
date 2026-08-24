package plugins

import (
	"reflect"
	"strings"
	"testing"

	"setpoint/internal/operation"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/clickhousechecks"
)

func TestFormalCatalogContainsOnlyExecutableReadOnlyChecks(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	metadata := registry.List()
	if len(metadata) != 9 {
		t.Fatalf("formal check count=%d", len(metadata))
	}
	checkItems := 0
	for _, current := range metadata {
		if current.Mode != plugin.ModeReadOnly || !registry.SupportsCheckExecution(current.ID) {
			t.Fatalf("non-executable or non-read-only check=%#v", current)
		}
		for _, definition := range current.Checks {
			if len(definition.SourceRefs) == 0 {
				t.Fatalf("formal check %s has no source reference", definition.ID)
			}
		}
		checkItems += len(current.Checks)
	}
	if checkItems != 84 {
		t.Fatalf("formal check items=%d, want 84", checkItems)
	}
	if len(registry.ListBundles()) != 9 {
		t.Fatalf("formal bundles=%#v", registry.ListBundles())
	}
	if len(registry.ListPolicies()) != 8 {
		t.Fatalf("formal policies=%#v", registry.ListPolicies())
	}
}

func TestRC1FormalCatalogIsCompleteAndReferenceSafe(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	plugins := registry.List()
	definitions := registry.ListDefinitions()
	bundles := registry.ListBundles()
	policies := registry.ListPolicies()
	if len(plugins) != 9 || len(definitions) != 84 || len(bundles) != 9 || len(policies) != 8 {
		t.Fatalf("formal catalog=%d plugins/%d checks/%d bundles/%d policies, want 9/84/9/8", len(plugins), len(definitions), len(bundles), len(policies))
	}
	if formalOperations := operation.NewRegistry().List(); len(formalOperations) != 0 {
		t.Fatalf("formal operation definitions=%#v, want empty", formalOperations)
	}
	pluginIDs := make(map[string]struct{}, len(plugins))
	for _, metadata := range plugins {
		if _, duplicate := pluginIDs[metadata.ID]; duplicate {
			t.Fatalf("duplicate owning plugin ID %q", metadata.ID)
		}
		pluginIDs[metadata.ID] = struct{}{}
		if strings.TrimSpace(metadata.Version) == "" || !registry.SupportsCheckExecution(metadata.ID) {
			t.Fatalf("owning plugin is not versioned and executable: %#v", metadata)
		}
	}
	checkIDs := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, duplicate := checkIDs[definition.ID]; duplicate {
			t.Fatalf("duplicate check ID %q", definition.ID)
		}
		checkIDs[definition.ID] = struct{}{}
		if strings.TrimSpace(definition.PluginID) == "" || strings.TrimSpace(definition.PluginVersion) == "" || len(definition.SourceRefs) == 0 {
			t.Fatalf("check has incomplete owner, version, or SourceRef: %#v", definition)
		}
		owner, exists := registry.Get(definition.PluginID)
		if !exists || owner.Version != definition.PluginVersion {
			t.Fatalf("check %s owner %s@%s is not registered", definition.ID, definition.PluginID, definition.PluginVersion)
		}
		assertUniqueNonEmpty(t, "check "+definition.ID+" SourceRef", definition.SourceRefs)
	}
	bundleIDs := make(map[string]struct{}, len(bundles))
	for _, bundle := range bundles {
		if _, duplicate := bundleIDs[bundle.ID]; duplicate {
			t.Fatalf("duplicate bundle ID %q", bundle.ID)
		}
		bundleIDs[bundle.ID] = struct{}{}
		assertUniqueNonEmpty(t, "bundle "+bundle.ID+" check", bundle.CheckIDs)
		for _, checkID := range bundle.CheckIDs {
			if _, exists := checkIDs[checkID]; !exists {
				t.Fatalf("bundle %s references unknown check %s", bundle.ID, checkID)
			}
		}
		resolved, err := registry.ResolveSelection(nil, []string{bundle.ID}, nil)
		if err != nil || !reflect.DeepEqual(resolved.CheckIDs, bundle.CheckIDs) {
			t.Fatalf("bundle %s resolution=%#v err=%v", bundle.ID, resolved, err)
		}
	}
	policyIDs := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if _, duplicate := policyIDs[policy.ID]; duplicate {
			t.Fatalf("duplicate policy ID %q", policy.ID)
		}
		policyIDs[policy.ID] = struct{}{}
		assertUniqueNonEmpty(t, "policy "+policy.ID+" check", policy.CheckIDs)
		assertUniqueNonEmpty(t, "policy "+policy.ID+" bundle", policy.BundleIDs)
		for _, checkID := range policy.CheckIDs {
			if _, exists := checkIDs[checkID]; !exists {
				t.Fatalf("policy %s references unknown check %s", policy.ID, checkID)
			}
		}
		for _, bundleID := range policy.BundleIDs {
			if _, exists := bundleIDs[bundleID]; !exists {
				t.Fatalf("policy %s references unknown bundle %s", policy.ID, bundleID)
			}
		}
		resolved, err := registry.ResolveSelection(nil, nil, []string{policy.ID})
		if err != nil || len(resolved.CheckIDs) == 0 {
			t.Fatalf("policy %s resolution=%#v err=%v", policy.ID, resolved, err)
		}
	}
}

func TestClickHouseReadinessPolicyExpandsWholeBundle(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.ResolveSelection(nil, nil, []string{"policy.clickhouse-migration-readiness"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Groups) != 1 || resolved.Groups[0].PluginID != clickhousechecks.ID || len(resolved.CheckIDs) != 13 {
		t.Fatalf("ClickHouse readiness resolution=%#v", resolved)
	}
}

func assertUniqueNonEmpty(t *testing.T, label string, values []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s reference is empty", label)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("%s reference %q is duplicated", label, value)
		}
		seen[value] = struct{}{}
	}
}

func TestM2Batch3PolicyExpandsOnlyItsEightChecks(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.ResolveSelection(nil, nil, []string{"policy.security-baseline-m2-batch3"})
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []plugin.ResolvedCheckGroup{
		{PluginID: "linux.network.icmp_redirects", CheckIDs: []string{"net.ipv4.conf.all.accept_redirects.persisted", "net.ipv4.conf.all.send_redirects.persisted", "net.ipv4.conf.default.accept_redirects.persisted", "net.ipv4.conf.default.send_redirects.persisted"}},
		{PluginID: "linux.network.source_route", CheckIDs: []string{"net.ipv4.conf.all.accept_source_route.persisted", "net.ipv4.conf.default.accept_source_route.persisted"}},
		{PluginID: "ssh.baseline.core", CheckIDs: []string{"ssh.listener.configured_ports_active", "ssh.listener.unexpected_ports"}},
	}
	if len(resolved.CheckIDs) != 8 || !reflect.DeepEqual(resolved.Groups, wantGroups) {
		t.Fatalf("M2 batch 3 resolution=%#v", resolved)
	}
}

func TestM2Batch2PolicyExpandsOnlyItsEightChecks(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.ResolveSelection(nil, nil, []string{"policy.security-baseline-m2-batch2"})
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []plugin.ResolvedCheckGroup{
		{PluginID: "linux.baseline.core", CheckIDs: []string{"login.motd"}},
		{PluginID: "nginx.baseline.core", CheckIDs: []string{"nginx.cors.allow_origin", "nginx.error_page_404", "nginx.header.referrer_policy", "nginx.header.x_content_type_options", "nginx.header.x_frame_options", "nginx.header.x_xss_protection", "nginx.location_alias_boundary"}},
	}
	if len(resolved.CheckIDs) != 8 || !reflect.DeepEqual(resolved.Groups, wantGroups) {
		t.Fatalf("M2 batch 2 resolution=%#v", resolved)
	}
}

func TestM2Batch1PolicyExpandsOnlyItsFifteenChecks(t *testing.T) {
	registry := plugin.NewCheckRegistry()
	if err := RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.ResolveSelection(nil, nil, []string{"policy.security-baseline-m2-batch1"})
	if err != nil {
		t.Fatal(err)
	}
	wantGroups := []plugin.ResolvedCheckGroup{
		{PluginID: "linux.files.permissions", CheckIDs: []string{"permissions.cron_spool", "permissions.group", "permissions.gshadow", "permissions.login_defs", "permissions.security_directory", "permissions.services", "permissions.wtmp"}},
		{PluginID: "linux.network.source_route", CheckIDs: []string{"net.ipv4.conf.all.accept_source_route", "net.ipv4.conf.default.accept_source_route"}},
		{PluginID: "linux.password.policy", CheckIDs: []string{"password.pwquality.digit_credit", "password.pwquality.lowercase_credit", "password.pwquality.min_length", "password.pwquality.other_credit", "password.pwquality.uppercase_credit", "password.warn_days"}},
	}
	if len(resolved.CheckIDs) != 15 || !reflect.DeepEqual(resolved.Groups, wantGroups) {
		t.Fatalf("M2 batch 1 resolution=%#v", resolved)
	}
}
