package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/futuretea/vsphere-proxy/internal/api"
	"github.com/futuretea/vsphere-proxy/internal/config"
	"github.com/futuretea/vsphere-proxy/internal/targets"
)

func main() {
	configPath := flag.String("config", "", "Path to the JSON configuration file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string) error {
	if path == "" {
		return fmt.Errorf("-config is required")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	manager := targets.New(cfg.Targets)
	server := &http.Server{Addr: cfg.Listen, Handler: api.New(manager)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownDone <- server.Shutdown(context.Background())
	}()
	err = server.ListenAndServe()
	stop()
	shutdownErr := <-shutdownDone
	closeErr := manager.Close(context.Background())
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	if shutdownErr != nil || closeErr != nil {
		return fmt.Errorf("failed to shut down server or target sessions")
	}
	return nil
}
