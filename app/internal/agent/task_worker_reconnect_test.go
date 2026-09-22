package agent

import (
	"context"
	"encoding/json"
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

func (runner *reconnectOperationRunner) Execute(context.Context, task.Resource) (task.OperationExecutionResult, error) {
	runner.calls++
	return runner.result, nil
}

type reconnectWorkerExecutor struct {
	bootID   string
	commands []executor.Command
}

func (fake *reconnectWorkerExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	fake.commands = append(fake.commands, command)
	switch {
	case command.Name == "reboot" && len(command.Args) == 0:
		return executor.Result{}, nil
	case command.Name == "cat" && reflect.DeepEqual(command.Args, []string{"--", "/proc/sys/kernel/random/boot_id"}):
		return executor.Result{Stdout: fake.bootID + "\n"}, nil
	default:
		return executor.Result{}, nil
	}
}

func TestTaskWorkerDurablyResumesReconnectWithoutMutationReplay(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		t.Run(string(action), func(t *testing.T) {
			metadata := (&actionTestDefinition{metadata: clickhouse.OperationMetadata()}).Metadata()
			resource := actionTask(t, metadata, action)
			resource.Status.Phase = task.PhaseClaimed
			handoff := &operation.ReconnectHandoff{
				Barrier: operation.StageBarrierAgentReconnect,
				Reboot: true, BootIDBefore: "boot-before",
			}
			result := task.OperationExecutionResult{
				OperationID: resource.Spec.OperationExecution.OperationID,
				RunID: resource.Spec.OperationExecution.RunID,
				Action: action,
				ParticipantNodeIDs: append([]string(nil), resource.Spec.OperationExecution.ParticipantNodeIDs...),
				StageID: resource.Spec.OperationExecution.Stage.ID,
				StageIndex: resource.Spec.OperationExecution.StageIndex,
				ExecutorNodeID: resource.Spec.OperationExecution.Stage.ExecutorNodeID,
			}
			if action == task.OperationActionApply {
				result.Apply = &operation.ApplyResult{
					Changed: true, Checkpoint: "os_cutover_persisted",
					State: operation.Artifact{SchemaVersion: "test.apply.v1", Payload: json.RawMessage(`{"mutation_state":"CHANGED"}`)},
					Reconnect: handoff,
				}
			} else {
				result.Rollback = &operation.RollbackResult{
					Restored: true, Checkpoint: "rollback_os_persisted",
					State: operation.Artifact{SchemaVersion: "test.rollback.v1", Payload: json.RawMessage(`{"mutation_state":"CHANGED"}`)},
					Reconnect: handoff,
				}
			}
			runner := &reconnectOperationRunner{result: result}
			remote := &planningWorkerRemote{resource: resource}
			journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			registry := operation.NewRegistry()
			if err := registry.Register(&actionTestDefinition{metadata: metadata}); err != nil {
				t.Fatal(err)
			}
			host := &reconnectWorkerExecutor{bootID: "boot-before"}
			first, err := NewTaskWorkerWithControlledOperations(remote, "node-1", "linux", testWorkerRegistry(t), registry, runner, host, journal, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := first.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			entry, found, err := journal.Load()
			if err != nil || !found || entry.State != journalReconnecting || entry.Submission == nil {
				t.Fatalf("journal=%#v found=%v err=%v", entry, found, err)
			}
			if runner.calls != 1 || len(remote.submissions) != 0 || countWorkerCommand(host.commands, "reboot") != 1 {
				t.Fatalf("calls=%d submissions=%d commands=%#v", runner.calls, len(remote.submissions), host.commands)
			}

			// Simulate a real Agent restart: the same durable journal survives, but
			// the machine boot identity has changed. The mutation runner must not
			// execute again.
			host.bootID = "boot-after"
			second, err := NewTaskWorkerWithControlledOperations(remote, "node-1", "linux", testWorkerRegistry(t), registry, runner, host, journal, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := second.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || len(remote.submissions) != 1 {
				t.Fatalf("mutation replayed or cached result not submitted: calls=%d submissions=%#v", runner.calls, remote.submissions)
			}
			submitted := remote.submissions[0].OperationExecutionResult
			if submitted == nil {
				t.Fatal("cached operation result is missing")
			}
			var completed *operation.ReconnectHandoff
			if action == task.OperationActionApply {
				completed = submitted.Apply.Reconnect
			} else {
				completed = submitted.Rollback.Reconnect
			}
			if completed == nil || completed.BootIDBefore != "boot-before" || completed.BootIDAfter != "boot-after" {
				t.Fatalf("completed reconnect=%#v", completed)
			}
			if _, found, err := journal.Load(); err != nil || found {
				t.Fatalf("journal was not cleared after cached submission: found=%v err=%v", found, err)
			}
		})
	}
}

func TestTaskWorkerReconnectDoesNotSubmitBeforeBootTransition(t *testing.T) {
	metadata := (&actionTestDefinition{metadata: clickhouse.OperationMetadata()}).Metadata()
	resource := actionTask(t, metadata, task.OperationActionApply)
	resource.Status.Phase = task.PhaseClaimed
	result := task.OperationExecutionResult{
		OperationID: resource.Spec.OperationExecution.OperationID,
		RunID: resource.Spec.OperationExecution.RunID,
		Action: task.OperationActionApply,
		ParticipantNodeIDs: append([]string(nil), resource.Spec.OperationExecution.ParticipantNodeIDs...),
		StageID: resource.Spec.OperationExecution.Stage.ID,
		StageIndex: resource.Spec.OperationExecution.StageIndex,
		ExecutorNodeID: resource.Spec.OperationExecution.Stage.ExecutorNodeID,
		Apply: &operation.ApplyResult{
			Changed: true, Checkpoint: "os_cutover_persisted",
			State: operation.Artifact{SchemaVersion: "test.apply.v1", Payload: json.RawMessage(`{"mutation_state":"CHANGED"}`)},
			Reconnect: &operation.ReconnectHandoff{Barrier: operation.StageBarrierAgentReconnect, Reboot: true, BootIDBefore: "boot-before"},
		},
	}
	runner := &reconnectOperationRunner{result: result}
	remote := &planningWorkerRemote{resource: resource}
	journal, err := NewTaskJournal(filepath.Join(t.TempDir(), "task-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(&actionTestDefinition{metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	host := &reconnectWorkerExecutor{bootID: "boot-before"}
	worker, err := NewTaskWorkerWithControlledOperations(remote, "node-1", "linux", testWorkerRegistry(t), registry, runner, host, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A subsequent loop before the host actually restarts must neither replay
	// the mutation nor submit a false reconnect success.
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || len(remote.submissions) != 0 {
		t.Fatalf("same-boot reconnect advanced early: calls=%d submissions=%#v", runner.calls, remote.submissions)
	}
}

func countWorkerCommand(commands []executor.Command, name string) int {
	count := 0
	for _, command := range commands {
		if command.Name == name {
			count++
		}
	}
	return count
}
