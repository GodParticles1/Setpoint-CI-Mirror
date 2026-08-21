package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"setpoint/internal/api"
	"setpoint/internal/app"
	"setpoint/internal/bootstrap"
	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
	"setpoint/internal/plugin"
	"setpoint/internal/plugins"
	"setpoint/internal/server"
	storage "setpoint/internal/storage/sqlite"
	"setpoint/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	configPath := flag.String("config", "", "path to optional JSON configuration")
	developmentChecks := flag.Bool("development-checks", false, "register development-only check metadata")
	flag.Parse()

	config, err := server.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	store, err := storage.Open(context.Background(), config.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close storage", "error", err)
		}
	}()

	registry := plugin.NewCheckRegistry()
	if err := plugins.RegisterFormal(registry); err != nil {
		return err
	}
	if *developmentChecks {
		for _, candidate := range plugin.DevelopmentChecks() {
			if err := registry.Register(candidate); err != nil {
				return fmt.Errorf("register development check: %w", err)
			}
		}
	}
	service, err := app.NewService(store, store, registry, config.OfflineAfter)
	if err != nil {
		return err
	}
	if err := service.SyncChecks(context.Background()); err != nil {
		return err
	}
	operationRegistry := operation.NewRegistry()
	if err := operationRegistry.Register(clickhouse.NewCatalogDescriptor()); err != nil {
		return err
	}
	operations, err := app.NewOperationsService(store, store, operationRegistry, config.OfflineAfter)
	if err != nil {
		return err
	}
	managementAPI, err := api.NewManagementHandlerWithOperations(store, service, operations, logger)
	if err != nil {
		return err
	}

	sshFactory, err := bootstrap.NewSSHFactory(10 * time.Second)
	if err != nil {
		return err
	}
	artifactProvider, err := bootstrap.NewDirectoryArtifactProvider(config.AgentArtifactsDirectory, version)
	if err != nil {
		return err
	}
	bootstrapService, err := bootstrap.NewService(sshFactory, artifactProvider, service, service, config.AgentAdvertiseURL)
	if err != nil {
		return err
	}
	managementAPI, err = api.WithNodeBootstrap(managementAPI, bootstrapService, logger)
	if err != nil {
		return err
	}

	agentHandler, err := api.NewAgentHandler(service, logger)
	if err != nil {
		return err
	}
	managementHandler, err := webui.New(managementAPI)
	if err != nil {
		return err
	}
	httpServer, err := server.New(config, api.ProtectManagement(managementHandler), agentHandler, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return httpServer.Run(ctx)
}
