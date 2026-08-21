package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRunnerHeartbeatsWhileTaskIsRunningAndWaitsOnShutdown(t *testing.T) {
	heartbeatObserved := make(chan struct{})
	var heartbeatOnce sync.Once
	remote := &scriptedRemote{heartbeat: func(int32) error {
		heartbeatOnce.Do(func() { close(heartbeatObserved) })
		return nil
	}}
	processor := &blockingTaskProcessor{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	runner := newTestRunner(t, remote)
	runner.taskProcessor = processor
	runner.heartbeatInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	result := runAsync(runner, ctx)
	waitSignalOrResult(t, processor.started, result, "task start")
	waitSignalOrResult(t, heartbeatObserved, result, "heartbeat during task")
	cancel()
	waitRunnerResult(t, result)
	select {
	case <-processor.stopped:
	default:
		t.Fatal("runner returned before task processor stopped")
	}
}

type blockingTaskProcessor struct {
	once    sync.Once
	started chan struct{}
	stopped chan struct{}
}

func (processor *blockingTaskProcessor) ProcessOne(ctx context.Context) error {
	processor.once.Do(func() { close(processor.started) })
	<-ctx.Done()
	close(processor.stopped)
	return ctx.Err()
}
