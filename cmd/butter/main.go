package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/plugin/builtin/requestlog"
	"github.com/temikus/butter/internal/version"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/provider/anthropic"
	"github.com/temikus/butter/internal/provider/openai"
	"github.com/temikus/butter/internal/provider/openrouter"
	"github.com/temikus/butter/internal/proxy"
	"github.com/temikus/butter/internal/transport"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.String())
		os.Exit(0)
	}

	configPath := flag.String("config", "config.yaml", "path to configuration file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	logger.Info("starting butter", "version", version.Version, "commit", version.Commit)

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Build provider registry.
	registry := provider.NewRegistry()

	// Create a shared HTTP client with connection pooling.
	httpClient := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: cfg.Server.WriteTimeout,
	}

	// Register configured providers.
	for name, provCfg := range cfg.Providers {
		switch name {
		case "openrouter":
			registry.Register(openrouter.New(provCfg.BaseURL, httpClient))
		case "openai":
			registry.Register(openai.New(provCfg.BaseURL, httpClient))
		case "anthropic":
			registry.Register(anthropic.New(provCfg.BaseURL, httpClient))
		default:
			logger.Warn("unknown provider, skipping", "provider", name)
		}
	}

	// Plugin system.
	pluginMgr := plugin.NewManager(logger)

	// Register built-in plugins based on config.
	if _, ok := cfg.Plugins["requestlog"]; ok {
		pluginMgr.Register(requestlog.New(logger))
	}

	if err := pluginMgr.InitAll(cfg.Plugins); err != nil {
		logger.Error("failed to initialize plugins", "error", err)
		os.Exit(1)
	}

	pluginChain := plugin.NewChain(pluginMgr, logger)

	engine := proxy.NewEngine(registry, cfg, logger, pluginChain)
	server := transport.NewServer(&cfg.Server, engine, logger, pluginChain)

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	pluginMgr.CloseAll()
	fmt.Println("butter stopped")
}
