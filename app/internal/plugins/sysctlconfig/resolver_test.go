package sysctlconfig

import "testing"

func TestResolveRequiresSystemdAndProcpsViewsToAgree(t *testing.T) {
	snapshot := Snapshot{files: []sourceFile{
		{root: "usr", base: "50-default.conf", contents: "net.ipv4.conf.all.accept_redirects = 1\n"},
		{root: "etc", base: "90-hardening.conf", contents: "net.ipv4.conf.all.accept_redirects = 0\n"},
	}}
	resolution := snapshot.Resolve("net.ipv4.conf.all.accept_redirects")
	if resolution.State != StateResolved || resolution.Value != "0" || len(resolution.Digest) != 64 {
		t.Fatalf("resolution=%#v", resolution)
	}

	snapshot.files = append(snapshot.files, sourceFile{root: "etc", base: "sysctl.conf", legacy: true, contents: "net.ipv4.conf.all.accept_redirects=1\n"})
	resolution = snapshot.Resolve("net.ipv4.conf.all.accept_redirects")
	if resolution.State != StateAmbiguous || resolution.Reason == "" {
		t.Fatalf("resolution=%#v", resolution)
	}
}

func TestResolveHonorsBasenameShadowingAndLexicalOrder(t *testing.T) {
	snapshot := Snapshot{files: []sourceFile{
		{root: "usr", base: "40-vendor.conf", contents: "net.ipv4.conf.all.send_redirects=1\n"},
		{root: "etc", base: "40-vendor.conf", contents: "net.ipv4.conf.all.send_redirects=0\n"},
		{root: "usr", base: "90-late.conf", contents: "net.ipv4.conf.all.send_redirects=1\n"},
		{root: "etc", base: "99-local.conf", contents: "net.ipv4.conf.all.send_redirects=0\n"},
	}}
	resolution := snapshot.Resolve("net.ipv4.conf.all.send_redirects")
	if resolution.State != StateResolved || resolution.Value != "0" {
		t.Fatalf("resolution=%#v", resolution)
	}
}

func TestResolveUsesLastAssignmentWithinFile(t *testing.T) {
	snapshot := Snapshot{files: []sourceFile{{
		root: "etc", base: "99-local.conf",
		contents: "net.ipv4.conf.default.send_redirects=1\nnet.ipv4.conf.default.send_redirects = 0 # approved\n",
	}}}
	resolution := snapshot.Resolve("net.ipv4.conf.default.send_redirects")
	if resolution.State != StateResolved || resolution.Value != "0" {
		t.Fatalf("resolution=%#v", resolution)
	}
}

func TestResolveIsConservativeForUnsupportedTargetSemantics(t *testing.T) {
	for name, contents := range map[string]string{
		"glob":       "net.ipv4.conf.*.accept_redirects=0\n",
		"slash":      "net/ipv4/conf/all/accept_redirects=0\n",
		"ignored":    "-net.ipv4.conf.all.accept_redirects=0\n",
		"nonboolean": "net.ipv4.conf.all.accept_redirects=disabled\n",
		"malformed":  "net.ipv4.conf.all.accept_redirects\n",
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := Snapshot{files: []sourceFile{{root: "etc", base: "99-local.conf", contents: contents}}}
			if resolution := snapshot.Resolve("net.ipv4.conf.all.accept_redirects"); resolution.State != StateAmbiguous {
				t.Fatalf("resolution=%#v", resolution)
			}
		})
	}
}

func TestResolveMissingAndUnsafeSourceAreDistinct(t *testing.T) {
	if resolution := (Snapshot{}).Resolve("net.ipv4.conf.all.accept_source_route"); resolution.State != StateMissing {
		t.Fatalf("resolution=%#v", resolution)
	}
	resolution := (Snapshot{issues: []string{"unsafe source"}}).Resolve("net.ipv4.conf.all.accept_source_route")
	if resolution.State != StateAmbiguous {
		t.Fatalf("resolution=%#v", resolution)
	}
}
