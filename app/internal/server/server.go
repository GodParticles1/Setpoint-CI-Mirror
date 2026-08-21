package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type Server struct {
	management      *http.Server
	agent           *http.Server
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

func New(config Config, managementHandler, agentHandler http.Handler, logger *slog.Logger) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if managementHandler == nil || agentHandler == nil || logger == nil {
		return nil, errors.New("management handler, Agent handler, and logger are required")
	}
	return &Server{
		management:      newHTTPServer(config.ManagementListenAddress, managementHandler, config),
		agent:           newHTTPServer(config.AgentListenAddress, agentHandler, config),
		shutdownTimeout: config.ShutdownTimeout,
		logger:          logger,
	}, nil
}

func newHTTPServer(address string, handler http.Handler, config Config) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaxHeaderBytes,
	}
}

func (server *Server) Run(ctx context.Context) error {
	managementListener, err := net.Listen("tcp", server.management.Addr)
	if err != nil {
		return fmt.Errorf("listen for management on %s: %w", server.management.Addr, err)
	}
	agentListener, err := net.Listen("tcp", server.agent.Addr)
	if err != nil {
		_ = managementListener.Close()
		return fmt.Errorf("listen for Agent on %s: %w", server.agent.Addr, err)
	}
	return server.Serve(ctx, managementListener, agentListener)
}

type serveResult struct {
	name string
	err  error
}

func (server *Server) Serve(ctx context.Context, managementListener, agentListener net.Listener) error {
	results := make(chan serveResult, 2)
	server.start("management", server.management, managementListener, results)
	server.start("agent", server.agent, agentListener, results)

	select {
	case result := <-results:
		shutdownErr := server.shutdown()
		other := <-results
		if result.err != nil && !errors.Is(result.err, http.ErrServerClosed) {
			return fmt.Errorf("serve %s HTTP: %w", result.name, result.err)
		}
		if other.err != nil && !errors.Is(other.err, http.ErrServerClosed) {
			return fmt.Errorf("serve %s HTTP: %w", other.name, other.err)
		}
		return shutdownErr
	case <-ctx.Done():
		shutdownErr := server.shutdown()
		for range 2 {
			result := <-results
			if result.err != nil && !errors.Is(result.err, http.ErrServerClosed) && shutdownErr == nil {
				shutdownErr = fmt.Errorf("serve %s HTTP during shutdown: %w", result.name, result.err)
			}
		}
		server.logger.Info("server stopped")
		return shutdownErr
	}
}

func (server *Server) start(name string, httpServer *http.Server, listener net.Listener, results chan<- serveResult) {
	go func() {
		server.logger.Info("server listener started", "listener", name, "address", listener.Addr().String())
		results <- serveResult{name: name, err: httpServer.Serve(listener)}
	}()
}

func (server *Server) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), server.shutdownTimeout)
	defer cancel()
	errorsByListener := make(chan error, 2)
	for _, httpServer := range []*http.Server{server.management, server.agent} {
		go func(current *http.Server) { errorsByListener <- current.Shutdown(ctx) }(httpServer)
	}
	var shutdownErr error
	for range 2 {
		shutdownErr = errors.Join(shutdownErr, <-errorsByListener)
	}
	if shutdownErr != nil {
		_ = server.management.Close()
		_ = server.agent.Close()
		return fmt.Errorf("shutdown HTTP listeners: %w", shutdownErr)
	}
	return nil
}
