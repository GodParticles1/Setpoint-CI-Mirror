package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	application "setpoint/internal/app"
	"setpoint/internal/checkrun"
	"setpoint/internal/domain"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/plugins/linuxbaseline"
	"setpoint/internal/plugins/linuxicmpredirects"
	"setpoint/internal/plugins/sshbaseline"
	"setpoint/internal/protocol"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/task"
)

type cancelFaultRepository struct {
	application.NodeRepository
	failTaskID string
	attempted  []string
}

func (repository *cancelFaultRepository) CancelTask(
	ctx context.Context,
	taskID string,
	canceledAt time.Time,
) (task.Resource, error) {
	repository.attempted = append(repository.attempted, taskID)
	if taskID == repository.failTaskID {
		return task.Resource{}, errors.New("injected cancellation storage detail")
	}
	return repository.NodeRepository.CancelTask(ctx, taskID, canceledAt)
}

func TestCancelCheckRunContinuesAfterPerTaskFailure(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "setpoint.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	registry := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(registry); err != nil {
		t.Fatal(err)
	}
	repository := &cancelFaultRepository{NodeRepository: store}
	service, err := application.NewService(repository, store, registry, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SyncChecks(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = store.RegisterNode(ctx, domain.Registration{
		AgentID: "cancel-node", Hostname: "cancel-node", OS: "linux", OSVersion: "test",
		Arch: "amd64", AgentVersion: "test", ReceivedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	run, _, err := service.CreateCheckRun(ctx, protocol.CreateCheckRunRequest{
		APIVersion: "setpoint.io/v1",
		Kind:       "ReadOnlyCheckRun",
		Metadata: protocol.CreateCheckRunMetadata{
			IdempotencyKey: "cancel-run-test",
			Name:           "best effort cancellation",
		},
		Spec: protocol.CreateCheckRunSpec{
			NodeIDs:  []string{"cancel-node"},
			CheckIDs: []string{linuxbaseline.ID, linuxicmpredirects.ID, sshbaseline.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Tasks) != 3 {
		t.Fatalf("tasks=%d", len(run.Tasks))
	}
	repository.failTaskID = run.Tasks[1].Metadata.ID

	response, err := service.CancelCheckRun(ctx, run.Metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(repository.attempted) != 3 {
		t.Fatalf("attempted=%v", repository.attempted)
	}
	if response.Report.TotalTasks != 3 || response.Report.CanceledTasks != 2 || response.Report.FailedTasks != 1 {
		t.Fatalf("report=%#v", response.Report)
	}
	if response.Report.CancelRequestedTasks != 0 || response.Report.AlreadyTerminalTasks != 0 || len(response.Report.Results) != 3 {
		t.Fatalf("report=%#v", response.Report)
	}

	var failed *checkrun.CancelTaskResult
	for index := range response.Report.Results {
		if response.Report.Results[index].TaskID == repository.failTaskID {
			failed = &response.Report.Results[index]
			break
		}
	}
	if failed == nil || failed.Outcome != checkrun.CancelOutcomeFailed || failed.Error == nil {
		t.Fatalf("failed result=%#v", failed)
	}
	if failed.Error.Code != "task_cancel_failed" || strings.Contains(failed.Error.Message, "injected") {
		t.Fatalf("failure was not stable and redacted: %#v", failed.Error)
	}
	if response.Run.Status.Phase != checkrun.PhaseRunning || response.Run.Status.Counts.CanceledTasks != 2 || response.Run.Status.Counts.PendingTasks != 1 {
		t.Fatalf("run status=%#v", response.Run.Status)
	}
}
