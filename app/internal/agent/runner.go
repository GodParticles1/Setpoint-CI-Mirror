package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"setpoint/internal/protocol"
)

type Remote interface {
	Register(context.Context, protocol.RegistrationRequest) error
	Heartbeat(context.Context, string) error
}

type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

type BackoffPolicy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

type Runner struct {
	remote            Remote
	registration      protocol.RegistrationRequest
	heartbeatInterval time.Duration
	taskPollInterval  time.Duration
	taskProcessor     TaskProcessor
	retry             RetryPolicy
	reconnect         BackoffPolicy
	logger            *slog.Logger
	sleep             func(context.Context, time.Duration) error
}

func NewRunner(
	config Config,
	remote Remote,
	taskProcessor TaskProcessor,
	agentID, agentVersion string,
	info SystemInfo,
	logger *slog.Logger,
) (*Runner, error) {
	if remote == nil || taskProcessor == nil || logger == nil {
		return nil, errors.New("agent remote, task processor and logger are required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Runner{
		remote: remote,
		registration: protocol.RegistrationRequest{
			AgentID: agentID, Hostname: info.Hostname, OS: info.OS,
			OSVersion: info.OSVersion, Arch: info.Arch, AgentVersion: agentVersion,
		},
		heartbeatInterval: config.HeartbeatInterval,
		taskPollInterval:  config.TaskPollInterval,
		taskProcessor:     taskProcessor,
		retry:             RetryPolicy{MaxAttempts: config.RetryMaxAttempts, InitialDelay: config.RetryInitialDelay, MaxDelay: config.RetryMaxDelay},
		reconnect:         BackoffPolicy{InitialDelay: config.ReconnectInitialDelay, MaxDelay: config.ReconnectMaxDelay},
		logger:            logger,
		sleep:             sleepContext,
	}, nil
}

func (runner *Runner) Run(ctx context.Context) error {
	reconnectDelay := runner.reconnect.InitialDelay
registrationLoop:
	for {
		err := retry(ctx, runner.retry, runner.sleep, func() error {
			return runner.remote.Register(ctx, runner.registration)
		}, runner.logger)
		if err != nil {
			if IsPermanentAuthenticationError(err) {
				return fmt.Errorf("Agent registration rejected: %w", err)
			}
			if ctx.Err() != nil {
				runner.logStopped()
				return nil
			}
			runner.logger.Warn("agent registration retry exhausted; waiting to reconnect",
				"agent_id", runner.registration.AgentID, "delay", reconnectDelay, "error", err)
			if err := runner.sleep(ctx, reconnectDelay); err != nil {
				if ctx.Err() != nil {
					runner.logStopped()
					return nil
				}
				return fmt.Errorf("wait to register agent: %w", err)
			}
			reconnectDelay = nextBackoff(reconnectDelay, runner.reconnect.MaxDelay)
			continue
		}

		runner.logger.Info("agent registered", "agent_id", runner.registration.AgentID)
		heartbeatTimer := time.NewTimer(runner.heartbeatInterval)
		taskTimer := time.NewTimer(0)
		taskContext, cancelTasks := context.WithCancel(ctx)
		var taskDone <-chan error
		for {
			select {
			case <-ctx.Done():
				stopTimer(heartbeatTimer)
				stopTimer(taskTimer)
				cancelTasks()
				waitTask(taskDone)
				runner.logStopped()
				return nil
			case <-taskTimer.C:
				done := make(chan error, 1)
				taskDone = done
				go func() {
					done <- runner.taskProcessor.ProcessOne(taskContext)
				}()
			case err := <-taskDone:
				taskDone = nil
				if err != nil {
					if IsPermanentAuthenticationError(err) {
						stopTimer(heartbeatTimer)
						stopTimer(taskTimer)
						cancelTasks()
						return fmt.Errorf("Agent task request rejected: %w", err)
					}
					if isFatalTaskError(err) {
						stopTimer(heartbeatTimer)
						stopTimer(taskTimer)
						cancelTasks()
						return fmt.Errorf("process Agent task: %w", err)
					}
					if ctx.Err() != nil {
						stopTimer(heartbeatTimer)
						stopTimer(taskTimer)
						cancelTasks()
						runner.logStopped()
						return nil
					}
					runner.logger.Warn("agent task cycle failed; waiting for next poll",
						"agent_id", runner.registration.AgentID, "delay", runner.taskPollInterval, "error", err)
				}
				taskTimer.Reset(runner.taskPollInterval)
			case <-heartbeatTimer.C:
				err := retry(ctx, runner.retry, runner.sleep, func() error {
					return runner.remote.Heartbeat(ctx, runner.registration.AgentID)
				}, runner.logger)
				if err == nil {
					reconnectDelay = runner.reconnect.InitialDelay
					heartbeatTimer.Reset(runner.heartbeatInterval)
					continue
				}
				if IsPermanentAuthenticationError(err) {
					stopTimer(taskTimer)
					cancelTasks()
					waitTask(taskDone)
					return fmt.Errorf("Agent heartbeat rejected: %w", err)
				}
				if ctx.Err() != nil {
					stopTimer(taskTimer)
					cancelTasks()
					waitTask(taskDone)
					runner.logStopped()
					return nil
				}
				runner.logger.Warn("agent heartbeat retry exhausted; waiting to re-register",
					"agent_id", runner.registration.AgentID, "delay", reconnectDelay, "error", err)
				stopTimer(taskTimer)
				cancelTasks()
				waitTask(taskDone)
				if err := runner.sleep(ctx, reconnectDelay); err != nil {
					if ctx.Err() != nil {
						runner.logStopped()
						return nil
					}
					return fmt.Errorf("wait to re-register agent: %w", err)
				}
				reconnectDelay = nextBackoff(reconnectDelay, runner.reconnect.MaxDelay)
				continue registrationLoop
			}
		}
	}
}

func (runner *Runner) logStopped() {
	runner.logger.Info("agent stopped", "agent_id", runner.registration.AgentID)
}

func retry(ctx context.Context, policy RetryPolicy, sleep func(context.Context, time.Duration) error, operation func() error, logger *slog.Logger) error {
	delay := policy.InitialDelay
	var lastError error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := operation(); err == nil {
			return nil
		} else {
			lastError = err
			if IsPermanentAuthenticationError(err) {
				return err
			}
		}
		if attempt == policy.MaxAttempts {
			break
		}
		logger.Warn("agent request failed; retrying", "attempt", attempt, "max_attempts", policy.MaxAttempts, "delay", delay, "error", lastError)
		if err := sleep(ctx, delay); err != nil {
			return err
		}
		delay = nextBackoff(delay, policy.MaxDelay)
	}
	return fmt.Errorf("retry attempts exhausted after %d attempts: %w", policy.MaxAttempts, lastError)
}

func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
