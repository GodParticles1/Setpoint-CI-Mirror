package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerProcessesTasksOnIndependentPollTimer(t *testing.T) {
	remote := &scriptedRemote{}
	processor := &observingTaskProcessor{called: make(chan struct{})}
	runner := newTestRunner(t, remote)
	runner.taskProcessor = processor
	runner.taskPollInterval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	result := runAsync(runner, ctx)
	waitSignalOrResult(t, processor.called, result, "task poll")
	cancel()
	waitRunnerResult(t, result)
	if processor.calls.Load() == 0 {
		t.Fatal("task processor was not called")
	}
}

func TestRunnerStopsOnFatalTaskStateError(t *testing.T) {
	runner := newTestRunner(t, &scriptedRemote{})
	runner.taskProcessor = &observingTaskProcessor{err: &fatalTaskError{err: errors.New("journal corrupt")}}
	result := runAsync(runner, context.Background())
	select {
	case err := <-result:
		if err == nil || !isFatalTaskError(err) {
			t.Fatalf("fatal task error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop on fatal task state")
	}
}

type noopTaskProcessor struct{}

func (noopTaskProcessor) ProcessOne(context.Context) error { return nil }

type observingTaskProcessor struct {
	calls  atomic.Int32
	once   sync.Once
	called chan struct{}
	err    error
}

func (processor *observingTaskProcessor) ProcessOne(context.Context) error {
	processor.calls.Add(1)
	if processor.called != nil {
		processor.once.Do(func() { close(processor.called) })
	}
	return processor.err
}
