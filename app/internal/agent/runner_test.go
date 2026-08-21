package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"setpoint/internal/protocol"
)

func TestRetryStopsAtConfiguredAttemptLimit(t *testing.T) {
	attempts := 0
	delays := make([]time.Duration, 0)
	err := retry(context.Background(), RetryPolicy{
		MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: 1500 * time.Millisecond,
	}, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}, func() error {
		attempts++
		return errors.New("unavailable")
	}, discardLogger())
	if err == nil || attempts != 3 {
		t.Fatalf("retry attempts=%d err=%v", attempts, err)
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != 1500*time.Millisecond {
		t.Fatalf("unexpected delays: %v", delays)
	}
}

func TestRetryStopsBeforeOperationWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := retry(ctx, RetryPolicy{MaxAttempts: 5, InitialDelay: time.Second, MaxDelay: time.Second}, sleepContext, func() error {
		attempts++
		return errors.New("unavailable")
	}, discardLogger())
	if !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRunnerRecoversFromInitialRegistrationFailureAndHeartbeats(t *testing.T) {
	heartbeatObserved := make(chan struct{})
	var heartbeatOnce sync.Once
	remote := &scriptedRemote{
		register: func(call int32) error {
			if call <= 2 {
				return errors.New("server unavailable")
			}
			return nil
		},
		heartbeat: func(int32) error {
			heartbeatOnce.Do(func() { close(heartbeatObserved) })
			return nil
		},
	}
	runner := newTestRunner(t, remote)
	runner.retry.MaxAttempts = 2
	runner.heartbeatInterval = time.Millisecond
	delays := make([]time.Duration, 0)
	runner.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := runAsync(runner, ctx)
	waitSignalOrResult(t, heartbeatObserved, result, "heartbeat after registration recovery")
	cancel()
	waitRunnerResult(t, result)
	if remote.registerCalls.Load() != 3 || remote.heartbeatCalls.Load() == 0 {
		t.Fatalf("register=%d heartbeat=%d", remote.registerCalls.Load(), remote.heartbeatCalls.Load())
	}
	if len(delays) < 2 || delays[0] != runner.retry.InitialDelay || delays[1] != runner.reconnect.InitialDelay {
		t.Fatalf("unexpected retry/reconnect delays: %v", delays)
	}
}

func TestRunnerReregistersAfterHeartbeatRetryExhaustion(t *testing.T) {
	secondRegistration := make(chan struct{})
	var registrationOnce sync.Once
	remote := &scriptedRemote{
		register: func(call int32) error {
			if call == 2 {
				registrationOnce.Do(func() { close(secondRegistration) })
			}
			return nil
		},
		heartbeat: func(int32) error { return errors.New("heartbeat unavailable") },
	}
	runner := newTestRunner(t, remote)
	runner.retry.MaxAttempts = 2
	runner.heartbeatInterval = time.Millisecond
	runner.sleep = func(context.Context, time.Duration) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	result := runAsync(runner, ctx)
	waitSignalOrResult(t, secondRegistration, result, "second registration")
	cancel()
	waitRunnerResult(t, result)
	if remote.heartbeatCalls.Load() < 2 {
		t.Fatalf("heartbeat attempts=%d, want at least 2", remote.heartbeatCalls.Load())
	}
}

func TestRunnerReconnectBackoffStopsAtMaximum(t *testing.T) {
	remote := &scriptedRemote{register: func(int32) error { return errors.New("unavailable") }}
	runner := newTestRunner(t, remote)
	runner.retry.MaxAttempts = 1
	runner.reconnect = BackoffPolicy{InitialDelay: 2 * time.Second, MaxDelay: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	delays := make([]time.Duration, 0, 4)
	runner.sleep = func(ctx context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		if len(delays) == 4 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("delays=%v, want %v", delays, want)
	}
	for index := range want {
		if delays[index] != want[index] {
			t.Fatalf("delays=%v, want %v", delays, want)
		}
	}
}

func TestRunnerCanceledContextExitsWithoutRemoteCall(t *testing.T) {
	remote := &scriptedRemote{register: func(int32) error { return errors.New("must not run") }}
	runner := newTestRunner(t, remote)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if remote.registerCalls.Load() != 0 {
		t.Fatalf("registration calls=%d", remote.registerCalls.Load())
	}
}

func TestRunnerDoesNotContinueAfterCancellation(t *testing.T) {
	heartbeatObserved := make(chan struct{})
	var heartbeatOnce sync.Once
	remote := &scriptedRemote{heartbeat: func(int32) error {
		heartbeatOnce.Do(func() { close(heartbeatObserved) })
		return nil
	}}
	runner := newTestRunner(t, remote)
	runner.heartbeatInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	result := runAsync(runner, ctx)
	waitSignalOrResult(t, heartbeatObserved, result, "heartbeat before cancellation")
	cancel()
	waitRunnerResult(t, result)
	registrations := remote.registerCalls.Load()
	heartbeats := remote.heartbeatCalls.Load()
	time.Sleep(10 * time.Millisecond)
	if remote.registerCalls.Load() != registrations || remote.heartbeatCalls.Load() != heartbeats {
		t.Fatalf("remote activity continued after cancellation: register %d->%d heartbeat %d->%d",
			registrations, remote.registerCalls.Load(), heartbeats, remote.heartbeatCalls.Load())
	}
}

type scriptedRemote struct {
	registerCalls  atomic.Int32
	heartbeatCalls atomic.Int32
	register       func(int32) error
	heartbeat      func(int32) error
}

func (remote *scriptedRemote) Register(context.Context, protocol.RegistrationRequest) error {
	call := remote.registerCalls.Add(1)
	if remote.register != nil {
		return remote.register(call)
	}
	return nil
}

func (remote *scriptedRemote) Heartbeat(context.Context, string) error {
	call := remote.heartbeatCalls.Add(1)
	if remote.heartbeat != nil {
		return remote.heartbeat(call)
	}
	return nil
}

func newTestRunner(t *testing.T, remote Remote) *Runner {
	t.Helper()
	config := DefaultConfig()
	config.RetryInitialDelay = time.Millisecond
	config.RetryMaxDelay = 2 * time.Millisecond
	config.ReconnectInitialDelay = 2 * time.Millisecond
	config.ReconnectMaxDelay = 4 * time.Millisecond
	runner, err := NewRunner(config, remote, noopTaskProcessor{}, "00000000-0000-4000-8000-000000000001", "test",
		SystemInfo{Hostname: "node", OS: "linux", OSVersion: "1", Arch: "amd64"}, discardLogger())
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	return runner
}

func runAsync(runner *Runner, ctx context.Context) <-chan error {
	result := make(chan error, 1)
	go func() { result <- runner.Run(ctx) }()
	return result
}

func waitSignalOrResult(t *testing.T, signal <-chan struct{}, result <-chan error, description string) {
	t.Helper()
	select {
	case <-signal:
		return
	case err := <-result:
		t.Fatalf("runner stopped before %s: %v", description, err)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitRunnerResult(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runner stopped with error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
