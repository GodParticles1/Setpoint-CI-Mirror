package sysctlconfig

import (
	"context"
	"errors"
	"strings"
	"testing"

	"setpoint/internal/executor"
)

func TestCollectReadsBoundedRegularRootOwnedSources(t *testing.T) {
	execution := emptyCollectorFixture()
	execution.results[collectorKey("test", "-d", "/etc/sysctl.d")] = executor.Result{}
	execution.errors[collectorKey("test", "-d", "/etc/sysctl.d")] = nil
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", "/etc/sysctl.d")] = executor.Result{Stdout: "755|root|root\n"}
	execution.results[collectorKey("find", "/etc/sysctl.d", "-mindepth", "1", "-maxdepth", "1", "-name", "*.conf", "-printf", "%y|%p\\n")] = executor.Result{Stdout: "f|/etc/sysctl.d/99-hardening.conf\n"}
	execution.errors[collectorKey("test", "-L", "/etc/sysctl.d/99-hardening.conf")] = exitError()
	execution.results[collectorKey("test", "-f", "/etc/sysctl.d/99-hardening.conf")] = executor.Result{}
	execution.errors[collectorKey("test", "-f", "/etc/sysctl.d/99-hardening.conf")] = nil
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", "/etc/sysctl.d/99-hardening.conf")] = executor.Result{Stdout: "644|root|root\n"}
	execution.results[collectorKey("cat", "--", "/etc/sysctl.d/99-hardening.conf")] = executor.Result{Stdout: "net.ipv4.conf.all.accept_redirects=0\n"}

	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	resolution := snapshot.Resolve("net.ipv4.conf.all.accept_redirects")
	if resolution.State != StateResolved || resolution.Value != "0" {
		t.Fatalf("resolution=%#v commands=%#v", resolution, execution.commands)
	}
}

func TestCollectConservativelyMarksSourceAnomalies(t *testing.T) {
	tests := map[string]func(*collectorFixture){
		"symlink directory": func(execution *collectorFixture) {
			execution.results[collectorKey("test", "-L", "/etc/sysctl.d")] = executor.Result{}
			execution.errors[collectorKey("test", "-L", "/etc/sysctl.d")] = nil
		},
		"unsafe directory mode": func(execution *collectorFixture) {
			execution.results[collectorKey("test", "-d", "/etc/sysctl.d")] = executor.Result{}
			execution.errors[collectorKey("test", "-d", "/etc/sysctl.d")] = nil
			execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", "/etc/sysctl.d")] = executor.Result{Stdout: "777|root|root\n"}
		},
		"unreadable file": func(execution *collectorFixture) {
			execution.results[collectorKey("test", "-d", "/etc/sysctl.d")] = executor.Result{}
			execution.errors[collectorKey("test", "-d", "/etc/sysctl.d")] = nil
			execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", "/etc/sysctl.d")] = executor.Result{Stdout: "755|root|root\n"}
			execution.results[collectorKey("find", "/etc/sysctl.d", "-mindepth", "1", "-maxdepth", "1", "-name", "*.conf", "-printf", "%y|%p\\n")] = executor.Result{Stdout: "f|/etc/sysctl.d/99-hardening.conf\n"}
			execution.errors[collectorKey("test", "-L", "/etc/sysctl.d/99-hardening.conf")] = exitError()
			execution.results[collectorKey("test", "-f", "/etc/sysctl.d/99-hardening.conf")] = executor.Result{}
			execution.errors[collectorKey("test", "-f", "/etc/sysctl.d/99-hardening.conf")] = nil
			execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", "/etc/sysctl.d/99-hardening.conf")] = executor.Result{Stdout: "644|root|root\n"}
			execution.errors[collectorKey("cat", "--", "/etc/sysctl.d/99-hardening.conf")] = exitError()
		},
	}
	for name, configure := range tests {
		t.Run(name, func(t *testing.T) {
			execution := emptyCollectorFixture()
			configure(execution)
			snapshot, err := Collect(context.Background(), execution)
			if err != nil {
				t.Fatal(err)
			}
			if resolution := snapshot.Resolve("net.ipv4.conf.all.accept_redirects"); resolution.State != StateAmbiguous {
				t.Fatalf("resolution=%#v", resolution)
			}
		})
	}
}

func TestCollectReturnsTechnicalCommandFailure(t *testing.T) {
	execution := emptyCollectorFixture()
	execution.errors[collectorKey("test", "-L", "/etc/sysctl.d")] = &executor.Error{Kind: executor.ErrorTimeout, Err: errors.New("deadline")}
	if _, err := Collect(context.Background(), execution); err == nil {
		t.Fatal("technical collection failure was suppressed")
	}
}

func TestProbeUsesPortableUnaryPredicateArguments(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		target string
		exists bool
	}{
		{name: "existing directory", kind: "-d", target: "/etc/sysctl.d", exists: true},
		{name: "missing directory", kind: "-d", target: "/run/sysctl.d", exists: false},
		{name: "regular file", kind: "-f", target: "/etc/sysctl.conf", exists: true},
		{name: "missing file", kind: "-f", target: "/etc/sysctl.d/missing.conf", exists: false},
		{name: "symlink", kind: "-L", target: "/etc/sysctl.d/link.conf", exists: true},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			execution := &collectorFixture{results: map[string]executor.Result{}, errors: map[string]error{}}
			key := collectorKey("test", current.kind, current.target)
			if current.exists {
				execution.results[key] = executor.Result{}
			} else {
				execution.errors[key] = exitError()
			}
			exists, err := probe(context.Background(), execution, current.kind, current.target)
			if err != nil || exists != current.exists {
				t.Fatalf("exists=%t err=%v commands=%#v", exists, err, execution.commands)
			}
			if len(execution.commands) != 1 || len(execution.commands[0].Args) != 2 || execution.commands[0].Args[1] != current.target {
				t.Fatalf("portable test argv was not preserved: %#v", execution.commands)
			}
		})
	}
}

func TestProbeRejectsUnsupportedPredicateAndUntrustedTargetsBeforeExecution(t *testing.T) {
	for _, current := range []struct {
		kind   string
		target string
	}{
		{kind: "-f", target: "etc/sysctl.conf"},
		{kind: "-f", target: "/tmp/unsafe.conf"},
		{kind: "-f", target: "/etc/sysctl.d/../unsafe.conf"},
		{kind: "-e", target: "/etc/sysctl.conf"},
	} {
		execution := &collectorFixture{results: map[string]executor.Result{}, errors: map[string]error{}}
		if _, err := probe(context.Background(), execution, current.kind, current.target); err == nil {
			t.Fatalf("unsupported probe was accepted: %#v", current)
		}
		if len(execution.commands) != 0 {
			t.Fatalf("unsupported probe reached the executor: %#v", execution.commands)
		}
	}
}

func TestCollectRefusesCandidateTraversalBeforeReading(t *testing.T) {
	execution := emptyCollectorFixture()
	execution.results[collectorKey("test", "-d", "/etc/sysctl.d")] = executor.Result{}
	execution.errors[collectorKey("test", "-d", "/etc/sysctl.d")] = nil
	execution.results[collectorKey("stat", "-c", "%a|%U|%G", "--", "/etc/sysctl.d")] = executor.Result{Stdout: "755|root|root\n"}
	execution.results[collectorKey("find", "/etc/sysctl.d", "-mindepth", "1", "-maxdepth", "1", "-name", "*.conf", "-printf", "%y|%p\\n")] = executor.Result{Stdout: "f|/etc/sysctl.d/../unsafe.conf\n"}

	snapshot, err := Collect(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if resolution := snapshot.Resolve("net.ipv4.conf.all.accept_redirects"); resolution.State != StateAmbiguous {
		t.Fatalf("traversal candidate was not rejected: %#v", resolution)
	}
	for _, command := range execution.commands {
		if command.Name == "cat" || (command.Name == "test" && len(command.Args) > 1 && strings.Contains(command.Args[1], "unsafe.conf")) {
			t.Fatalf("traversal candidate reached a file probe: %#v", command)
		}
	}
}

type collectorFixture struct {
	results  map[string]executor.Result
	errors   map[string]error
	commands []executor.Command
}

func emptyCollectorFixture() *collectorFixture {
	execution := &collectorFixture{results: map[string]executor.Result{}, errors: map[string]error{}}
	for _, root := range sourceRoots {
		execution.errors[collectorKey("test", "-L", root.path)] = exitError()
		execution.errors[collectorKey("test", "-d", root.path)] = exitError()
	}
	execution.errors[collectorKey("test", "-L", "/etc/sysctl.conf")] = exitError()
	execution.errors[collectorKey("test", "-f", "/etc/sysctl.conf")] = exitError()
	return execution
}

func (execution *collectorFixture) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.commands = append(execution.commands, command)
	lookup := collectorKey(command.Name, command.Args...)
	result, resultExists := execution.results[lookup]
	err, errorExists := execution.errors[lookup]
	if !resultExists && !errorExists {
		return executor.Result{}, errors.New("unexpected command: " + lookup)
	}
	return result, err
}

func collectorKey(name string, args ...string) string {
	return name + "\x00" + strings.Join(args, "\x00")
}

func exitError() error {
	return &executor.Error{Kind: executor.ErrorExit, Result: executor.Result{ExitCode: 1}, Err: errors.New("exit status 1")}
}
