package trustedexec

import "testing"

func TestConfiguredRootRejectsRelativeTraversalAndTemporaryPaths(t *testing.T) {
	for _, value := range []string{
		"opt/vendor/bin",
		"/opt/vendor/../tmp/bin",
		"/tmp/vendor/bin",
		"/var/tmp/vendor/bin",
	} {
		if err := ValidateConfiguredPath(value); err == nil {
			t.Fatalf("trusted executable root %q was accepted", value)
		}
	}
}

func TestConfiguredRootNormalizesSortsAndDeduplicatesPaths(t *testing.T) {
	values, err := NormalizeConfiguredPaths([]string{" /opt/vendor/z ", "/opt/vendor/a", "/opt/vendor/a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != "/opt/vendor/a" || values[1] != "/opt/vendor/z" {
		t.Fatalf("normalized paths=%v", values)
	}
}

func TestCanonicalRootsRejectsCallerControlledScopeAndDuplicates(t *testing.T) {
	if _, err := CanonicalRoots([]Root{{Path: "/opt/vendor/bin", Scope: ScopeBuiltIn, Source: "caller"}}); err == nil {
		t.Fatal("caller-controlled built-in scope was accepted")
	}
	root := Root{Path: "/opt/vendor/bin", Scope: ScopeNode, Source: "node:test"}
	if _, err := CanonicalRoots([]Root{root, root}); err == nil {
		t.Fatal("duplicate frozen root was accepted")
	}
}
