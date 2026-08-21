package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

func TestTaskWorkerExecutesOnceAndResubmitsCachedResult(t *testing.T) {
	resource := workerTask(task.PhaseClaimed)
	remote := &workerRemote{claimed: &resource, submitErrors: []error{errors.New("server unavailable"), nil}}
	execution := &workerExecutor{}
	worker, journal := newWorkerForTest(t, remote, execution)

	err := worker.ProcessOne(context.Background())
	var remoteError *taskRemoteError
	if !errors.As(err, &remoteError) {
		t.Fatalf("first process error=%v", err)
	}
	firstExecutionCalls := execution.callCount()
	if firstExecutionCalls == 0 || len(remote.submissions) != 1 {
		t.Fatalf("execution calls=%d submissions=%d", execution.callCount(), len(remote.submissions))
	}
	if _, found, err := journal.Load(); err != nil || !found {
		t.Fatalf("cached journal found=%v err=%v", found, err)
	}

	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatalf("resubmit cached result: %v", err)
	}
	if execution.callCount() != firstExecutionCalls || len(remote.submissions) != 2 || remote.claimCalls != 3 {
		t.Fatalf("duplicate execution calls=%d submissions=%d claims=%d",
			execution.callCount(), len(remote.submissions), remote.claimCalls)
	}
	if remote.submissions[1].Phase != task.PhaseSucceeded ||
		remote.submissions[1].Result.State != task.CheckCompleted ||
		len(remote.submissions[1].Result.Items) != len(linuxicmpredirects.New().Metadata().Checks) {
		t.Fatalf("unexpected result: %#v", remote.submissions[1])
	}
	if _, found, err := journal.Load(); err != nil || found {
		t.Fatalf("acknowledged journal found=%v err=%v", found, err)
	}
}

func TestTaskWorkerDoesNotRepeatInterruptedExecution(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		seed       journalState
		claimPhase task.Phase
	}{
		{name: "executing journal", seed: journalExecuting},
		{name: "running server task", claimPhase: task.PhaseRunning},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resource := workerTask(testCase.claimPhase)
			remote := &workerRemote{}
			execution := &workerExecutor{}
			worker, journal := newWorkerForTest(t, remote, execution)
			if testCase.seed != "" {
				resource = workerTask(task.PhaseRunning)
				if err := journal.Save(taskJournalEntry{Version: 1, State: testCase.seed, Task: resource}); err != nil {
					t.Fatal(err)
				}
			} else {
				remote.claimed = &resource
			}
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if execution.callCount() != 0 || len(remote.submissions) != 1 {
				t.Fatalf("execution calls=%d submissions=%d", execution.callCount(), len(remote.submissions))
			}
			submission := remote.submissions[0]
			if submission.Phase != task.PhaseFailed || submission.Result.Error == nil ||
				submission.Result.Error.Code != "agent_execution_interrupted" {
				t.Fatalf("unexpected interrupted result: %#v", submission)
			}
		})
	}
}

func TestTaskWorkerHonorsCancellationBeforeExecution(t *testing.T) {
	resource := workerTask(task.PhaseCancelRequested)
	remote := &workerRemote{claimed: &resource}
	execution := &workerExecutor{}
	worker, _ := newWorkerForTest(t, remote, execution)
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if execution.callCount() != 0 || len(remote.submissions) != 1 {
		t.Fatalf("execution calls=%d submissions=%d", execution.callCount(), len(remote.submissions))
	}
	if remote.submissions[0].Phase != task.PhaseCanceled ||
		remote.submissions[0].Result.Error == nil ||
		remote.submissions[0].Result.Error.Code != "task_canceled" {
		t.Fatalf("unexpected canceled result: %#v", remote.submissions[0])
	}
}

func TestTaskWorkerTreatsJournalFailureAsFatal(t *testing.T) {
	remote := &workerRemote{}
	execution := &workerExecutor{}
	worker, journal := newWorkerForTest(t, remote, execution)
	if err := os.WriteFile(journal.path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := worker.ProcessOne(context.Background())
	if !isFatalTaskError(err) || execution.callCount() != 0 || remote.claimCalls != 0 {
		t.Fatalf("fatal=%v execution=%d claims=%d", err, execution.callCount(), remote.claimCalls)
	}
}

func TestTaskWorkerFailsClosedWhenFrozenRootsCannotBeApplied(t *testing.T) {
	registry := testWorkerRegistry(t)
	metadata, _ := registry.Get(linuxicmpredirects.ID)
	contract, digest, err := plugin.FreezeExecutionContract(
		metadata, nil, json.RawMessage(`{}`),
		[]trustedexec.Root{{Path: "/opt/company/bin", Scope: trustedexec.ScopeNode, Source: "node:agent-1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	execution := &workerExecutor{}
	worker := &TaskWorker{registry: registry, executor: execution, system: "linux"}
	resource := workerTask(task.PhaseRunning)
	resource.Spec.Execution = &contract
	resource.Spec.ContractDigest = digest
	if _, err := worker.executeTaskCheck(context.Background(), resource); err == nil {
		t.Fatal("task executed without an executor capable of applying its frozen trust roots")
	}
	if execution.callCount() != 0 {
		t.Fatalf("executor calls=%d", execution.callCount())
	}
}

func newWorkerForTest(t *testing.T, remote *workerRemote, execution *workerExecutor) (*TaskWorker, *TaskJournal) {
	t.Helper()
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewTaskWorker(remote, "agent-1", "linux", testWorkerRegistry(t), execution, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return worker, journal
}

func testWorkerRegistry(t *testing.T) *plugin.CheckRegistry {
	t.Helper()
	registry := plugin.NewCheckRegistry()
	if err := registry.Register(linuxicmpredirects.New()); err != nil {
		t.Fatal(err)
	}
	return registry
}

func workerTask(phase task.Phase) task.Resource {
	now := time.Now().UTC()
	return task.Resource{
		APIVersion: "setpoint.io/v1", Kind: task.KindReadOnlyCheckTask,
		Metadata: task.Metadata{ID: "task-1", IdempotencyKey: "idem-1", CreatedAt: now},
		Spec:     task.Spec{NodeID: "agent-1", PluginID: linuxicmpredirects.ID, Parameters: json.RawMessage(`{}`)},
		Status:   task.Status{Phase: phase, ClaimID: "claim-1", Attempt: 1, UpdatedAt: now},
	}
}

type workerRemote struct {
	mu           sync.Mutex
	claimed      *task.Resource
	claimCalls   int
	ackCalls     int
	submitErrors []error
	submissions  []task.ResultSubmission
}

func (remote *workerRemote) ClaimTask(context.Context, string) (*task.Resource, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.claimCalls++
	if remote.claimed == nil {
		return nil, nil
	}
	result := task.Clone(*remote.claimed)
	remote.claimed = nil
	return &result, nil
}

func (remote *workerRemote) AcknowledgeTask(_ context.Context, _, _, _ string) (task.Resource, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.ackCalls++
	return workerTask(task.PhaseRunning), nil
}

func (remote *workerRemote) SubmitTaskResult(
	_ context.Context,
	_, _ string,
	submission task.ResultSubmission,
) (task.Resource, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.submissions = append(remote.submissions, submission)
	call := len(remote.submissions) - 1
	if call < len(remote.submitErrors) && remote.submitErrors[call] != nil {
		return task.Resource{}, remote.submitErrors[call]
	}
	return workerTask(submission.Phase), nil
}

type workerExecutor struct {
	mu       sync.Mutex
	commands []executor.Command
}

func (execution *workerExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	execution.mu.Lock()
	defer execution.mu.Unlock()
	execution.commands = append(execution.commands, command)
	return executor.Result{Stdout: "0\n", ExitCode: 0}, nil
}

func (execution *workerExecutor) callCount() int {
	execution.mu.Lock()
	defer execution.mu.Unlock()
	return len(execution.commands)
}
