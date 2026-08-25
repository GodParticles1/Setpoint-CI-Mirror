package sysctlrepair

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

const fakeSysctlDropIn = "/etc/sysctl.d/99-setpoint-test.conf"

type fakeSysctlExecutor struct {
	key              string
	runtime          string
	dropInPersistent string
	legacyPersistent string
	writes           []string
}

func (fake *fakeSysctlExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	if command.Name == "sysctl" && reflect.DeepEqual(command.Args, []string{"-n", fake.key}) {
		return executor.Result{Stdout: fake.runtime + "\n", ExitCode: 0}, nil
	}
	if command.Name == "sysctl" && len(command.Args) == 2 && command.Args[0] == "-w" {
		if command.Args[1] == fake.key+"=0" {
			fake.runtime = "0"
			fake.writes = append(fake.writes, command.Args[1])
			return executor.Result{Stdout: command.Args[1] + "\n", ExitCode: 0}, nil
		}
		if command.Args[1] == fake.key+"=1" {
			fake.runtime = "1"
			fake.writes = append(fake.writes, command.Args[1])
			return executor.Result{Stdout: command.Args[1] + "\n", ExitCode: 0}, nil
		}
	}
	if command.Name == "test" && len(command.Args) == 2 {
		kind, path := command.Args[0], command.Args[1]
		exists := false
		switch {
		case kind == "-L":
			exists = false
		case kind == "-d" && path == "/etc/sysctl.d":
			exists = true
		case kind == "-f" && path == fakeSysctlDropIn:
			exists = true
		case kind == "-f" && path == "/etc/sysctl.conf" && fake.legacyPersistent != "":
			exists = true
		}
		if exists {
			return executor.Result{ExitCode: 0}, nil
		}
		result := executor.Result{ExitCode: 1}
		return result, &executor.Error{Kind: executor.ErrorExit, Result: result, Err: errors.New("not present")}
	}
	if command.Name == "stat" && len(command.Args) == 4 && command.Args[0] == "-c" && command.Args[1] == "%a|%U|%G" && command.Args[2] == "--" {
		switch command.Args[3] {
		case "/etc/sysctl.d":
			return executor.Result{Stdout: "755|root|root\n", ExitCode: 0}, nil
		case fakeSysctlDropIn, "/etc/sysctl.conf":
			return executor.Result{Stdout: "644|root|root\n", ExitCode: 0}, nil
		}
	}
	if command.Name == "find" && reflect.DeepEqual(command.Args,
		[]string{"/etc/sysctl.d", "-mindepth", "1", "-maxdepth", "1", "-name", "*.conf", "-printf", "%y|%p\\n"}) {
		return executor.Result{Stdout: "f|" + fakeSysctlDropIn + "\n", ExitCode: 0}, nil
	}
	if command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", fakeSysctlDropIn}) {
		return executor.Result{Stdout: fake.key + " = " + fake.dropInPersistent + "\n", ExitCode: 0}, nil
	}
	if command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/etc/sysctl.conf"}) && fake.legacyPersistent != "" {
		return executor.Result{Stdout: fake.key + " = " + fake.legacyPersistent + "\n", ExitCode: 0}, nil
	}
	return executor.Result{}, errors.New("unexpected command")
}

type validLease struct{ lease operation.LockLease }

func (lease validLease) Current() operation.LockLease { return lease.lease }
func (lease validLease) Validate(at time.Time) error  { return operation.ValidateLease(lease.lease, at) }

func TestBoundedRuntimeRepairApplyVerifyRollback(t *testing.T) {
	checkID := "net.ipv4.conf.all.accept_redirects.persisted"
	key := allowedChecks[checkID]
	commandExecutor := &fakeSysctlExecutor{key: key, runtime: "1", dropInPersistent: "0"}
	definition, err := NewDefinition(commandExecutor)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewRestoreProvider(commandExecutor)
	if err != nil {
		t.Fatal(err)
	}
	parameters := []byte(`{"check_id":"` + checkID + `","target_value":"runtime=0; persisted=0"}`)
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	runtime := operation.RuntimeInput{Executor: commandExecutor, Parameters: parameters, System: "linux", Targets: targets}
	discovery, err := definition.Discover(context.Background(), operation.DiscoverInput{Runtime: runtime})
	if err != nil || !discovery.Applicable {
		t.Fatalf("discovery=%#v err=%v", discovery, err)
	}
	precheck, err := definition.Precheck(context.Background(), operation.PrecheckInput{Runtime: runtime, Discovery: discovery})
	if err != nil || !precheck.Passed {
		t.Fatalf("precheck=%#v err=%v", precheck, err)
	}
	plan, err := definition.Plan(context.Background(), operation.PlanInput{Runtime: runtime, Discovery: discovery, Precheck: precheck})
	if err != nil {
		t.Fatal(err)
	}
	impact, err := definition.Impact(context.Background(), operation.ImpactInput{Runtime: runtime, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	restorePoint, err := provider.Create(context.Background(), operation.RestorePointRequest{OperationID: ID, RunID: "run-1", Targets: targets, Plan: plan, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	verifiedRestore, err := provider.Verify(context.Background(), restorePoint)
	if err != nil || !verifiedRestore.Passed {
		t.Fatalf("restore verify=%#v err=%v", verifiedRestore, err)
	}
	now := time.Now().UTC()
	lease := validLease{operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: "node||node-1||"}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}}
	applied, err := definition.Apply(context.Background(), operation.ApplyInput{Runtime: runtime, Plan: plan, Impact: impact, RestorePoint: restorePoint, Lease: lease})
	if err != nil || !applied.Changed || commandExecutor.runtime != "0" {
		t.Fatalf("apply=%#v runtime=%s err=%v", applied, commandExecutor.runtime, err)
	}
	verification, err := definition.Verify(context.Background(), operation.VerifyInput{Runtime: runtime, Plan: plan, Apply: applied})
	if err != nil || !verification.Passed {
		t.Fatalf("verify=%#v err=%v", verification, err)
	}
	rolledBack, err := definition.Rollback(context.Background(), operation.RollbackInput{Runtime: runtime, Plan: plan, Apply: applied, RestorePoint: restorePoint, Lease: lease})
	if err != nil || !rolledBack.Restored || commandExecutor.runtime != "1" {
		t.Fatalf("rollback=%#v runtime=%s err=%v", rolledBack, commandExecutor.runtime, err)
	}
	rollbackVerification, err := definition.VerifyRollback(context.Background(), operation.VerifyRollbackInput{Runtime: runtime, Plan: plan, Rollback: rolledBack, RestorePoint: restorePoint})
	if err != nil || !rollbackVerification.Passed {
		t.Fatalf("verify rollback=%#v err=%v", rollbackVerification, err)
	}
	if !reflect.DeepEqual(commandExecutor.writes, []string{key + "=0", key + "=1"}) {
		t.Fatalf("writes=%v", commandExecutor.writes)
	}
}

func TestApplyFailsClosedWhenPersistentEvidenceChanges(t *testing.T) {
	checkID := "net.ipv4.conf.all.accept_redirects.persisted"
	key := allowedChecks[checkID]
	commandExecutor := &fakeSysctlExecutor{key: key, runtime: "1", dropInPersistent: "0"}
	definition, _ := NewDefinition(commandExecutor)
	parameters := []byte(`{"check_id":"` + checkID + `","target_value":"runtime=0; persisted=0"}`)
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	runtime := operation.RuntimeInput{Executor: commandExecutor, Parameters: parameters, System: "linux", Targets: targets}
	discovery, _ := definition.Discover(context.Background(), operation.DiscoverInput{Runtime: runtime})
	precheck, _ := definition.Precheck(context.Background(), operation.PrecheckInput{Runtime: runtime, Discovery: discovery})
	plan, _ := definition.Plan(context.Background(), operation.PlanInput{Runtime: runtime, Discovery: discovery, Precheck: precheck})
	impact, _ := definition.Impact(context.Background(), operation.ImpactInput{Runtime: runtime, Plan: plan})
	commandExecutor.dropInPersistent = "1"
	now := time.Now().UTC()
	lease := validLease{operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: "node||node-1||"}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}}
	_, err := definition.Apply(context.Background(), operation.ApplyInput{Runtime: runtime, Plan: plan, Impact: impact, Lease: lease})
	if err == nil {
		t.Fatal("expected persisted evidence drift to block Apply")
	}
	if len(commandExecutor.writes) != 0 || commandExecutor.runtime != "1" {
		t.Fatalf("mutation occurred: writes=%v runtime=%s", commandExecutor.writes, commandExecutor.runtime)
	}
}

func TestApplyFailsClosedWhenSysctlLoadingViewsDisagree(t *testing.T) {
	checkID := "net.ipv4.conf.all.accept_redirects.persisted"
	key := allowedChecks[checkID]
	commandExecutor := &fakeSysctlExecutor{key: key, runtime: "1", dropInPersistent: "0"}
	definition, _ := NewDefinition(commandExecutor)
	parameters := []byte(`{"check_id":"` + checkID + `","target_value":"runtime=0; persisted=0"}`)
	targets := []operation.Target{{Kind: operation.TargetNode, NodeID: "node-1"}}
	runtime := operation.RuntimeInput{Executor: commandExecutor, Parameters: parameters, System: "linux", Targets: targets}
	discovery, err := definition.Discover(context.Background(), operation.DiscoverInput{Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	precheck, err := definition.Precheck(context.Background(), operation.PrecheckInput{Runtime: runtime, Discovery: discovery})
	if err != nil || !precheck.Passed {
		t.Fatalf("precheck=%#v err=%v", precheck, err)
	}
	plan, err := definition.Plan(context.Background(), operation.PlanInput{Runtime: runtime, Discovery: discovery, Precheck: precheck})
	if err != nil {
		t.Fatal(err)
	}
	impact, err := definition.Impact(context.Background(), operation.ImpactInput{Runtime: runtime, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	commandExecutor.legacyPersistent = "1"
	now := time.Now().UTC()
	lease := validLease{operation.LockLease{ID: "lease-1", OwnerID: "run-1", Resources: []operation.LockResource{{Key: "node||node-1||"}}, AcquiredAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}}
	_, err = definition.Apply(context.Background(), operation.ApplyInput{Runtime: runtime, Plan: plan, Impact: impact, Lease: lease})
	if err == nil {
		t.Fatal("expected disagreeing systemd-sysctl and procps --system views to block Apply")
	}
	if len(commandExecutor.writes) != 0 || commandExecutor.runtime != "1" {
		t.Fatalf("mutation occurred with disagreeing loading views: writes=%v runtime=%s", commandExecutor.writes, commandExecutor.runtime)
	}
}

func TestCatalogNormalizerRejectsUnapprovedTarget(t *testing.T) {
	catalog := NewCatalogDescriptor()
	if _, err := catalog.NormalizeParameters([]byte(`{"check_id":"net.ipv4.conf.all.accept_redirects.persisted","target_value":"runtime=1; persisted=1"}`)); err == nil {
		t.Fatal("expected unapproved target to be rejected")
	}
	if _, err := catalog.NormalizeParameters([]byte(`{"check_id":"other.check","target_value":"runtime=0; persisted=0"}`)); err == nil {
		t.Fatal("expected unapproved check to be rejected")
	}
}
