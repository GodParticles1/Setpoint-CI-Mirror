package sysctlconfig

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"setpoint/internal/executor"
)

const (
	kylinLegacyLink = "/etc/sysctl.d/99-sysctl.conf"
	kylinTargetKey  = "net.ipv4.conf.default.accept_redirects"
)

func TestCollectAcceptsBoundedKylinLegacyBridge(t *testing.T) {
	execution := safeLegacyBridgeFixture(kylinTargetKey + "=0\n")
	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	resolution := snapshot.Resolve(kylinTargetKey)
	if resolution.State != StateResolved || resolution.Value != "0" {
		t.Fatalf("resolution=%#v issues=%v", resolution, snapshot.issues)
	}
	if len(snapshot.files) != 2 {
		t.Fatalf("files=%#v", snapshot.files)
	}
	bridge, legacy := snapshot.files[0], snapshot.files[1]
	if bridge.root != "etc" || bridge.base != "99-sysctl.conf" || bridge.legacy {
		t.Fatalf("sysctl.d source identity was not preserved: %#v", bridge)
	}
	if legacy.root != "etc" || legacy.base != "sysctl.conf" || !legacy.legacy {
		t.Fatalf("legacy source identity changed: %#v", legacy)
	}
	systemd, procps := snapshot.resolveView(kylinTargetKey, false), snapshot.resolveView(kylinTargetKey, true)
	if systemd.state != StateResolved || systemd.value != "0" || procps.state != StateResolved || procps.value != "0" {
		t.Fatalf("systemd=%#v procps=%#v", systemd, procps)
	}
}

func TestCollectAcceptsAbsoluteLegacyBridgeTarget(t *testing.T) {
	execution := safeLegacyBridgeFixture(kylinTargetKey + "=0\n")
	execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: legacyBridgeTarget + "\n"}
	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if resolution := snapshot.Resolve(kylinTargetKey); resolution.State != StateResolved || resolution.Value != "0" {
		t.Fatalf("resolution=%#v issues=%v", resolution, snapshot.issues)
	}
}

func TestCollectRejectsUnsafeLegacyBridgeForms(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*collectorFixture)
		forbiddenPath string
	}{
		{name: "outside tmp", forbiddenPath: "/tmp/evil.conf", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "/tmp/evil.conf\n"}
		}},
		{name: "outside etc passwd", forbiddenPath: "/etc/passwd", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "/etc/passwd\n"}
		}},
		{name: "target symlink chain", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("test", "-L", legacyBridgeTarget)] = executor.Result{}
			execution.errors[collectorKey("test", "-L", legacyBridgeTarget)] = nil
		}},
		{name: "target nonroot owner", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", legacyBridgeTarget)] = executor.Result{Stdout: "644|app|root\n"}
		}},
		{name: "target unsafe mode", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", legacyBridgeTarget)] = executor.Result{Stdout: "666|root|root\n"}
		}},
		{name: "target nonregular", configure: func(execution *collectorFixture) {
			delete(execution.results, collectorKey("test", "-f", legacyBridgeTarget))
			execution.errors[collectorKey("test", "-f", legacyBridgeTarget)] = exitError()
		}},
		{name: "link nonroot owner", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", kylinLegacyLink)] = executor.Result{Stdout: "777|app|root\n"}
		}},
		{name: "multiline link", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "../sysctl.conf\n/etc/passwd\n"}
		}},
		{name: "empty link", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "\n"}
		}},
		{name: "cyclic link", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "99-sysctl.conf\n"}
		}},
		{name: "traversal outside fixed target", forbiddenPath: "/tmp/sysctl.conf", configure: func(execution *collectorFixture) {
			execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "../../tmp/sysctl.conf\n"}
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			execution := safeLegacyBridgeFixture(kylinTargetKey + "=0\n")
			current.configure(execution)
			snapshot, err := Collect(context.Background(), execution)
			if err != nil {
				t.Fatal(err)
			}
			if resolution := snapshot.Resolve(kylinTargetKey); resolution.State != StateAmbiguous {
				t.Fatalf("unsafe bridge did not fail closed: %#v", resolution)
			}
			if len(snapshot.issues) == 0 {
				t.Fatal("unsafe bridge did not create a Snapshot issue")
			}
			if current.forbiddenPath != "" && commandUsesPath(execution.commands, current.forbiddenPath) {
				t.Fatalf("outside target reached executor: %#v", execution.commands)
			}
		})
	}
}

func TestCollectCountsRejectedSymlinksAgainstCandidateLimit(t *testing.T) {
	execution := emptyCollectorFixture()
	configureSourceRoot(execution, "/etc/sysctl.d")
	var listing strings.Builder
	for index := 0; index <= maximumCandidates; index++ {
		candidate := fmt.Sprintf("/etc/sysctl.d/%03d-link.conf", index)
		fmt.Fprintf(&listing, "l|%s\n", candidate)
		execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", candidate)] = executor.Result{Stdout: "777|root|root\n"}
		execution.results[collectorKey("readlink", "--", candidate)] = executor.Result{Stdout: "/tmp/evil.conf\n"}
	}
	execution.results[findKey("/etc/sysctl.d")] = executor.Result{Stdout: listing.String()}
	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if !containsIssue(snapshot.issues, "persistent sysctl source count exceeds the evaluation limit") {
		t.Fatalf("candidate bound issue missing: %v", snapshot.issues)
	}
	readlinks := 0
	for _, command := range execution.commands {
		if command.Name == "readlink" {
			readlinks++
		}
	}
	if readlinks != maximumCandidates {
		t.Fatalf("evaluated symlinks=%d want=%d", readlinks, maximumCandidates)
	}
}

func TestCollectCountsLegacyBridgeContentsAgainstLimit(t *testing.T) {
	execution := emptyCollectorFixture()
	configureSourceRoot(execution, "/etc/sysctl.d")
	contents := kylinTargetKey + "=0\n# " + strings.Repeat("x", 9000) + "\n"
	configureLegacyTarget(execution, contents)
	var listing strings.Builder
	for index := 0; index < 60; index++ {
		candidate := fmt.Sprintf("/etc/sysctl.d/%02d-sysctl.conf", index)
		fmt.Fprintf(&listing, "l|%s\n", candidate)
		execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", candidate)] = executor.Result{Stdout: "777|root|root\n"}
		execution.results[collectorKey("readlink", "--", candidate)] = executor.Result{Stdout: "../sysctl.conf\n"}
	}
	execution.results[findKey("/etc/sysctl.d")] = executor.Result{Stdout: listing.String()}
	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if !containsIssue(snapshot.issues, "persistent sysctl source contents exceed the evaluation limit") {
		t.Fatalf("content bound issue missing: %v", snapshot.issues)
	}
}

func TestAcceptedLegacyBridgePreservesTargetAmbiguity(t *testing.T) {
	for name, contents := range map[string]string{
		"glob":       "net.ipv4.conf.*.accept_redirects=0\n",
		"slash":      "net/ipv4/conf/default/accept_redirects=0\n",
		"ignored":    "-net.ipv4.conf.default.accept_redirects=0\n",
		"nonboolean": "net.ipv4.conf.default.accept_redirects=disabled\n",
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := Collect(context.Background(), safeLegacyBridgeFixture(contents))
			if err != nil {
				t.Fatal(err)
			}
			if resolution := snapshot.Resolve(kylinTargetKey); resolution.State != StateAmbiguous {
				t.Fatalf("unsupported semantics were accepted: %#v", resolution)
			}
		})
	}
}

func TestAcceptedLegacyBridgePreservesLoadingViewDisagreement(t *testing.T) {
	execution := safeLegacyBridgeFixture(kylinTargetKey + "=0\n")
	override := "/etc/sysctl.d/zz-override.conf"
	execution.results[findKey("/etc/sysctl.d")] = executor.Result{Stdout: "l|" + kylinLegacyLink + "\nf|" + override + "\n"}
	configureRegularSource(execution, override, kylinTargetKey+"=1\n")
	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if resolution := snapshot.Resolve(kylinTargetKey); resolution.State != StateAmbiguous {
		t.Fatalf("disagreeing loading views were accepted: %#v", resolution)
	}
}

func safeLegacyBridgeFixture(contents string) *collectorFixture {
	execution := emptyCollectorFixture()
	configureSourceRoot(execution, "/etc/sysctl.d")
	execution.results[findKey("/etc/sysctl.d")] = executor.Result{Stdout: "l|" + kylinLegacyLink + "\n"}
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", kylinLegacyLink)] = executor.Result{Stdout: "777|root|root\n"}
	execution.results[collectorKey("readlink", "--", kylinLegacyLink)] = executor.Result{Stdout: "../sysctl.conf\n"}
	configureLegacyTarget(execution, contents)
	return execution
}

func configureSourceRoot(execution *collectorFixture, root string) {
	execution.results[collectorKey("test", "-d", root)] = executor.Result{}
	execution.errors[collectorKey("test", "-d", root)] = nil
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", root)] = executor.Result{Stdout: "755|root|root\n"}
}

func configureLegacyTarget(execution *collectorFixture, contents string) {
	execution.results[collectorKey("test", "-f", legacyBridgeTarget)] = executor.Result{}
	execution.errors[collectorKey("test", "-f", legacyBridgeTarget)] = nil
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", legacyBridgeTarget)] = executor.Result{Stdout: "644|root|root\n"}
	execution.results[collectorKey("cat", "--", legacyBridgeTarget)] = executor.Result{Stdout: contents}
}

func configureRegularSource(execution *collectorFixture, target, contents string) {
	execution.errors[collectorKey("test", "-L", target)] = exitError()
	execution.results[collectorKey("test", "-f", target)] = executor.Result{}
	execution.errors[collectorKey("test", "-f", target)] = nil
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", target)] = executor.Result{Stdout: "644|root|root\n"}
	execution.results[collectorKey("cat", "--", target)] = executor.Result{Stdout: contents}
}

func findKey(root string) string {
	return collectorKey("find", root, "-mindepth", "1", "-maxdepth", "1", "-name", "*.conf", "-printf", "%y|%p\\n")
}

func commandUsesPath(commands []executor.Command, target string) bool {
	for _, command := range commands {
		for _, argument := range command.Args {
			if argument == target {
				return true
			}
		}
	}
	return false
}

func containsIssue(issues []string, expected string) bool {
	for _, issue := range issues {
		if issue == expected {
			return true
		}
	}
	return false
}
