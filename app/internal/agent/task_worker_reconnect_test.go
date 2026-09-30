package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type reconnectOperationRunner struct {
	result task.OperationExecutionResult
	calls  int
}

func (r *reconnectOperationRunner) Execute(context.Context, task.Resource) (task.OperationExecutionResult, error) {
	r.calls++
	return r.result, nil
}

type reconnectWorkerExecutor struct {
	bootID       string
	commands     []executor.Command
	rebootErr    error
	bootErr      error
	bootResult   *executor.Result
	beforeReboot func()
}

func (e *reconnectWorkerExecutor) Execute(ctx context.Context, c executor.Command) (executor.Result, error) {
	e.commands = append(e.commands, c)
	if _, ok := ctx.Deadline(); !ok {
		return executor.Result{}, errors.New("command has no deadline")
	}
	switch {
	case c.Name == "reboot" && len(c.Args) == 0:
		if e.beforeReboot != nil {
			e.beforeReboot()
		}
		return executor.Result{}, e.rebootErr
	case c.Name == "cat" && reflect.DeepEqual(c.Args, []string{"--", "/proc/sys/kernel/random/boot_id"}):
		if e.bootResult != nil {
			return *e.bootResult, e.bootErr
		}
		return executor.Result{Stdout: e.bootID + "\n"}, e.bootErr
	default:
		return executor.Result{}, errors.New("unexpected command")
	}
}
func rebootCount(e *reconnectWorkerExecutor) int {
	n := 0
	for _, c := range e.commands {
		if c.Name == "reboot" {
			n++
		}
	}
	return n
}

type reconnectRemote struct {
	planningWorkerRemote
	loseAck bool
}

func (r *reconnectRemote) SubmitTaskResult(ctx context.Context, agentID, id string, s task.ResultSubmission) (task.Resource, error) {
	result, err := r.planningWorkerRemote.SubmitTaskResult(ctx, agentID, id, s)
	if r.loseAck {
		r.loseAck = false
		return task.Resource{}, errors.New("acknowledgment lost")
	}
	return result, err
}

func reconnectFixture(t *testing.T, action task.OperationAction) (*TaskWorker, *reconnectOperationRunner, *reconnectWorkerExecutor, *reconnectRemote) {
	t.Helper()
	metadata := clickhouse.OperationMetadata()
	resource := actionTask(t, metadata, action)
	resource.Status.Phase = task.PhaseClaimed
	contract := resource.Spec.OperationExecution
	handoff := &operation.ReconnectHandoff{Barrier: operation.StageBarrierAgentReconnect, Reboot: true, BootIDBefore: "boot-before"}
	artifact := operation.Artifact{SchemaVersion: "test.mutation.v1", Payload: json.RawMessage(`{"owned":"fixture"}`)}
	result := task.OperationExecutionResult{OperationID: contract.OperationID, RunID: contract.RunID, Action: action, ParticipantNodeIDs: contract.ParticipantNodeIDs, StageID: contract.Stage.ID, StageIndex: contract.StageIndex, ExecutorNodeID: contract.Stage.ExecutorNodeID}
	if action == task.OperationActionApply {
		result.Apply = &operation.ApplyResult{Changed: true, MutationState: operation.MutationChanged, Checkpoint: "apply_checkpoint", State: artifact, Reconnect: handoff}
	} else {
		result.Rollback = &operation.RollbackResult{Restored: true, MutationState: operation.MutationMayHaveChanged, Checkpoint: "rollback_checkpoint", State: artifact, Reconnect: handoff}
	}
	runner := &reconnectOperationRunner{result: result}
	remote := &reconnectRemote{planningWorkerRemote: planningWorkerRemote{resource: resource}}
	host := &reconnectWorkerExecutor{bootID: "boot-before"}
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(&actionTestDefinition{metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	worker, err := NewTaskWorkerWithControlledOperations(remote, "node-1", "linux", testWorkerRegistry(t), registry, runner, host, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return worker, runner, host, remote
}
func reopenWorker(t *testing.T, w *TaskWorker) *TaskWorker {
	t.Helper()
	journal, err := NewTaskJournal(w.journal.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewTaskWorkerWithControlledOperations(w.remote, w.agentID, w.system, w.registry, w.operations, w.execution, w.executor, journal, w.commandTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return restarted
}

func TestReconnectJournalRestartAndLostAcknowledgmentNeverReplay(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		t.Run(string(action), func(t *testing.T) {
			worker, runner, host, remote := reconnectFixture(t, action)
			host.beforeReboot = func() {
				entry, found, err := worker.journal.Load()
				if err != nil || !found || entry.State != journalReconnecting {
					t.Fatalf("reboot before durable journal: %#v %v", entry, err)
				}
				if !reflect.DeepEqual(*entry.Submission.OperationExecutionResult, runner.result) {
					t.Fatal("journal changed typed evidence")
				}
			}
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			worker = reopenWorker(t, worker)
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || len(remote.submissions) != 0 || rebootCount(host) != 1 {
				t.Fatal("same-boot restart replayed or submitted")
			}
			host.bootID = "boot-after"
			remote.loseAck = true
			if err := worker.ProcessOne(context.Background()); err == nil {
				t.Fatal("lost ack must surface")
			}
			entry, found, err := worker.journal.Load()
			if err != nil || !found || entry.State != journalCompleted {
				t.Fatalf("completed result not durable: %#v %v", entry, err)
			}
			handoff, err := reconnectHandoff(entry.Submission)
			if err != nil || handoff.BootIDAfter != "boot-after" {
				t.Fatalf("proof=%#v %v", handoff, err)
			}
			worker = reopenWorker(t, worker)
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || rebootCount(host) != 1 || len(remote.submissions) != 2 || !reflect.DeepEqual(remote.submissions[0], remote.submissions[1]) {
				t.Fatal("retry changed result or replayed mutation")
			}
			if _, found, err := worker.journal.Load(); err != nil || found {
				t.Fatalf("acknowledged journal remains: %v", err)
			}
		})
	}
}

func TestReconnectWithoutTypedStateFailsClosed(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		t.Run(string(action), func(t *testing.T) {
			worker, runner, host, remote := reconnectFixture(t, action)
			if runner.result.Apply != nil {
				runner.result.Apply.MutationState = ""
			} else {
				runner.result.Rollback.MutationState = ""
			}
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if rebootCount(host) != 0 || len(remote.submissions) != 1 || remote.submissions[0].Phase != task.PhaseFailed {
				t.Fatal("untyped reconnect accepted")
			}
		})
	}
}

func TestReconnectJournalRejectsInvalidStateOnReopen(t *testing.T) {
	worker, _, _, _ := reconnectFixture(t, task.OperationActionApply)
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	entry, _, err := worker.journal.Load()
	if err != nil {
		t.Fatal(err)
	}
	entry.Submission.OperationExecutionResult.Apply.MutationState = ""
	if err := worker.journal.Save(entry); err == nil {
		t.Fatal("invalid reconnect journal saved")
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker.journal.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := reopenWorker(t, worker).ProcessOne(context.Background()); !isFatalTaskError(err) {
		t.Fatalf("corrupt reconnect accepted: %v", err)
	}
}

func TestReconnectBootObservationFailuresRetainPendingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result executor.Result
		err    error
	}{
		{"empty", executor.Result{}, nil}, {"malformed", executor.Result{Stdout: "two identities"}, nil},
		{"nonzero", executor.Result{Stdout: "new-boot", ExitCode: 1}, nil}, {"truncated", executor.Result{Stdout: "new-boot", StdoutTruncated: true}, nil},
		{"read_error", executor.Result{}, errors.New("read failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker, runner, host, remote := reconnectFixture(t, task.OperationActionApply)
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			host.bootResult = &tc.result
			host.bootErr = tc.err
			if err := reopenWorker(t, worker).ProcessOne(context.Background()); err == nil {
				t.Fatal("boot error hidden")
			}
			entry, found, err := worker.journal.Load()
			if err != nil || !found || entry.State != journalReconnecting || runner.calls != 1 || len(remote.submissions) != 0 || rebootCount(host) != 1 {
				t.Fatal("boot error lost evidence or submitted/replayed")
			}
		})
	}
}

func TestReconnectRebootFailureRetainsTypedEvidenceAsFailure(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		t.Run(string(action), func(t *testing.T) {
			worker, _, host, remote := reconnectFixture(t, action)
			host.rebootErr = errors.New("reboot rejected")
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(remote.submissions) != 1 || remote.submissions[0].Phase != task.PhaseFailed {
				t.Fatal("reboot failure reported success")
			}
			result := remote.submissions[0].OperationExecutionResult
			if result.Error == nil || result.Error.Code != "operation_reconnect_reboot_failed" {
				t.Fatal("failure code lost")
			}
			if action == task.OperationActionApply && (result.Apply == nil || result.Apply.MutationState != operation.MutationChanged) {
				t.Fatal("apply evidence lost")
			}
			if action == task.OperationActionRollback && (result.Rollback == nil || result.Rollback.Restored || result.Rollback.MutationState != operation.MutationMayHaveChanged) {
				t.Fatal("rollback evidence lost or optimistic")
			}
		})
	}
}

func TestReconnectJournalWriteFailurePreventsReboot(t *testing.T) {
	worker, runner, host, remote := reconnectFixture(t, task.OperationActionApply)
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block journal mkdir"), 0600); err != nil {
		t.Fatal(err)
	}
	worker.journal.path = filepath.Join(blocker, "journal.json")
	submission := task.ResultSubmission{ClaimID: remote.resource.Status.ClaimID, Phase: task.PhaseSucceeded, OperationExecutionResult: &runner.result}
	if err := worker.cacheReconnectAndReboot(context.Background(), remote.resource, submission); !isFatalTaskError(err) {
		t.Fatalf("journal failure=%v", err)
	}
	if rebootCount(host) != 0 || len(remote.submissions) != 0 {
		t.Fatal("reboot/submission despite failed persistence")
	}
}

func TestReconnectRejectsStaleBootIdentityWithoutReboot(t *testing.T) {
	worker, _, host, remote := reconnectFixture(t, task.OperationActionApply)
	host.bootID = "unexpected-boot"
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rebootCount(host) != 0 || len(remote.submissions) != 1 || remote.submissions[0].Phase != task.PhaseFailed || remote.submissions[0].OperationExecutionResult.Apply == nil {
		t.Fatal("stale identity rebooted or lost failure evidence")
	}
}

func TestFailedRollbackWorkerTransportsEvidenceWithoutReboot(t *testing.T) {
	worker, runner, host, remote := reconnectFixture(t, task.OperationActionRollback)
	runner.result.Rollback.Restored = false
	runner.result.Error = &task.Failure{Code: "rollback_failed", Message: "partial recovery"}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rebootCount(host) != 0 || len(remote.submissions) != 1 || remote.submissions[0].Phase != task.PhaseFailed || !reflect.DeepEqual(remote.submissions[0].OperationExecutionResult, &runner.result) {
		t.Fatal("failed rollback changed evidence or initiated reboot")
	}
}
