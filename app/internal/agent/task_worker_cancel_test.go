package agent

import (
	"context"
	"testing"
	"time"

	"setpoint/internal/task"
)

func TestTaskWorkerHonorsCancellationBeforeResultSubmission(t *testing.T) {
	execution := &workerExecutor{}
	remote := &cancelRaceRemote{}
	worker, err := NewTaskWorker(
		remote, "agent-1", "linux", testWorkerRegistry(t), execution,
		mustTaskJournal(t), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if execution.callCount() == 0 || len(remote.submissions) != 1 {
		t.Fatalf("execution calls=%d submissions=%d", execution.callCount(), len(remote.submissions))
	}
	if remote.submissions[0].Phase != task.PhaseCanceled ||
		remote.submissions[0].Result.Error == nil ||
		remote.submissions[0].Result.Error.Code != "task_canceled" {
		t.Fatalf("unexpected canceled result: %#v", remote.submissions[0])
	}
}

func mustTaskJournal(t *testing.T) *TaskJournal {
	t.Helper()
	journal, err := NewTaskJournal(t.TempDir() + "/task-journal.json")
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

type cancelRaceRemote struct {
	claimCalls  int
	submissions []task.ResultSubmission
}

func (remote *cancelRaceRemote) ClaimTask(context.Context, string) (*task.Resource, error) {
	remote.claimCalls++
	phase := task.PhaseClaimed
	if remote.claimCalls > 1 {
		phase = task.PhaseCancelRequested
	}
	resource := workerTask(phase)
	return &resource, nil
}

func (remote *cancelRaceRemote) AcknowledgeTask(context.Context, string, string, string) (task.Resource, error) {
	return workerTask(task.PhaseRunning), nil
}

func (remote *cancelRaceRemote) SubmitTaskResult(
	_ context.Context,
	_, _ string,
	submission task.ResultSubmission,
) (task.Resource, error) {
	remote.submissions = append(remote.submissions, submission)
	return workerTask(submission.Phase), nil
}
