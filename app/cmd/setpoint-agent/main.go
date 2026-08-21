package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"setpoint/internal/agent"
	"setpoint/internal/executor"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/protocol"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(os.Args[1:], logger); err != nil {
		logger.Error("agent exited", "error", err)
		os.Exit(1)
	}
}

func run(args []string, logger *slog.Logger) error {
	if len(args) > 0 && args[0] == "runtime-probe" {
		return runRuntimeProbe(args[1:])
	}
	configPath := flag.String("config", "", "path to optional JSON configuration")
	rotateCredential := flag.Bool("rotate-credential", false, "rotate the enrolled Agent credential and exit")
	if err := flag.CommandLine.Parse(args); err != nil {
		return err
	}
	config, err := agent.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	agentID, err := agent.LoadOrCreateID(config.IdentityPath)
	if err != nil {
		return err
	}
	systemInfo, err := agent.CollectSystemInfo()
	if err != nil {
		return err
	}
	client, err := agent.NewClient(config.ServerURL, &http.Client{Timeout: config.RequestTimeout})
	if err != nil {
		return err
	}
	if _, newlyEnrolled, err := agent.BootstrapCredential(context.Background(), config, client, agentID); err != nil {
		return err
	} else if newlyEnrolled {
		if err := reexecAfterEnrollment(); err != nil {
			return err
		}
	}
	if *rotateCredential {
		return agent.RotateAndPersistCredential(context.Background(), config, client, protocol.RegistrationRequest{
			AgentID: agentID, Hostname: systemInfo.Hostname, OS: systemInfo.OS,
			OSVersion: systemInfo.OSVersion, Arch: systemInfo.Arch, AgentVersion: version,
		})
	}
	registry := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(registry); err != nil {
		return err
	}
	commandExecutor, err := executor.NewOSExecutor(0)
	if err != nil {
		return err
	}
	queryClient, err := clickhouse.NewExecutorClient(commandExecutor)
	if err != nil {
		return err
	}
	planningDefinition, err := clickhouse.NewPlanningDefinition(queryClient)
	if err != nil {
		return err
	}
	operationRegistry := operation.NewRegistry()
	if err := operationRegistry.Register(planningDefinition); err != nil {
		return err
	}
	journal, err := agent.NewTaskJournal(config.TaskJournalPath)
	if err != nil {
		return err
	}
	taskWorker, err := agent.NewTaskWorkerWithOperations(
		client, agentID, runtime.GOOS, registry, operationRegistry, commandExecutor, journal, config.CommandTimeout)
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(config, client, taskWorker, agentID, version, systemInfo, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
}

func runRuntimeProbe(args []string) error {
	if len(args) != 2 || args[0] != "--server-url" {
		return errors.New("runtime-probe requires exactly one --server-url")
	}
	flags := flag.NewFlagSet("runtime-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	serverURL := flags.String("server-url", "", "Setpoint Agent listener URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *serverURL == "" {
		return errors.New("runtime-probe requires exactly one --server-url")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return agent.ProbeRuntime(ctx, *serverURL, client)
}
