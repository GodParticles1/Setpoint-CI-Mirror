package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/task"
)

type reconnectProcessObservation struct {
	RunnerCalls int                            `json:"runner_calls"`
	RebootCalls int                            `json:"reboot_calls"`
	Submissions int                            `json:"submissions"`
	Phase       task.Phase                     `json:"phase"`
	Result      *task.OperationExecutionResult `json:"result"`
}

// This subprocess uses the real on-disk journal, with a fake local executor.
// Only the Agent process is restarted; no physical reboot is performed.
func TestReconnectProcessHelper(t *testing.T) {
	path := os.Getenv("SETPOINT_TEST_RECONNECT_JOURNAL")
	if path == "" {
		return
	}
	journal, err := NewTaskJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := journal.Load()
	if err != nil || !found {
		t.Fatalf("load: found=%v err=%v", found, err)
	}
	registry := operation.NewRegistry()
	if err := registry.Register(&actionTestDefinition{metadata: clickhouse.OperationMetadata()}); err != nil {
		t.Fatal(err)
	}
	runner := &reconnectOperationRunner{}
	host := &reconnectWorkerExecutor{bootID: os.Getenv("SETPOINT_TEST_RECONNECT_BOOT")}
	remote := &reconnectRemote{planningWorkerRemote: planningWorkerRemote{resource: entry.Task}}
	worker, err := NewTaskWorkerWithControlledOperations(remote, "node-1", "linux", testWorkerRegistry(t), registry, runner, host, journal, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	observation := reconnectProcessObservation{RunnerCalls: runner.calls, RebootCalls: rebootCount(host), Submissions: len(remote.submissions)}
	if observation.Submissions > 0 {
		observation.Phase = remote.submissions[0].Phase
		observation.Result = remote.submissions[0].OperationExecutionResult
	}
	if err := json.NewEncoder(os.Stdout).Encode(observation); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestReconnectSeparateProcessBeforeAndAfterBootTransition(t *testing.T) {
	for _, action := range []task.OperationAction{task.OperationActionApply, task.OperationActionRollback} {
		t.Run(string(action), func(t *testing.T) {
			worker, runner, _, _ := reconnectFixture(t, action)
			if err := worker.ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, boot := range []string{"boot-before", "boot-after"} {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReconnectProcessHelper$")
				command.Env = append(os.Environ(), "SETPOINT_TEST_RECONNECT_JOURNAL="+worker.journal.path, "SETPOINT_TEST_RECONNECT_BOOT="+boot)
				output, err := command.CombinedOutput()
				cancel()
				if err != nil {
					t.Fatalf("restart at %s: %v\n%s", boot, err, output)
				}
				var observed reconnectProcessObservation
				if err := json.Unmarshal(output, &observed); err != nil {
					t.Fatalf("decode: %v\n%s", err, output)
				}
				if observed.RunnerCalls != 0 || observed.RebootCalls != 0 {
					t.Fatalf("process restart replayed: %#v", observed)
				}
				if boot == "boot-before" {
					if observed.Submissions != 0 {
						t.Fatal("process submitted before boot changed")
					}
				} else {
					if observed.Submissions != 1 || observed.Phase != task.PhaseSucceeded || observed.Result == nil {
						t.Fatalf("final result: %#v", observed)
					}
					submission := task.ResultSubmission{OperationExecutionResult: observed.Result}
					handoff, err := reconnectHandoff(&submission)
					if err != nil || handoff == nil || handoff.BootIDAfter != boot {
						t.Fatalf("boot proof=%#v err=%v", handoff, err)
					}
					if observed.Result.Apply != nil && observed.Result.Apply.MutationState != runner.result.Apply.MutationState {
						t.Fatal("Apply state changed across process")
					}
					if observed.Result.Rollback != nil && observed.Result.Rollback.MutationState != runner.result.Rollback.MutationState {
						t.Fatal("Rollback state changed across process")
					}
				}
			}
			if _, found, err := worker.journal.Load(); err != nil || found {
				t.Fatalf("journal not acknowledged: %v", err)
			}
		})
	}
}
