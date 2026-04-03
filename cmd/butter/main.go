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

	"github.com/temikus/butter/internal/appkey"
	"github.com/temikus/butter/internal/cache"
	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/plugin/builtin/metrics"
	"github.com/temikus/butter/internal/plugin/builtin/ratelimit"
	"github.com/temikus/butter/internal/plugin/builtin/requestlog"
	"github.com/temikus/butter/internal/plugin/builtin/tracing"
	pluginwasm "github.com/temikus/butter/internal/plugin/wasm"
	"github.com/temikus/butter/internal/version"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/provider/anthropic"
	"github.com/temikus/butter/internal/provider/fireworks"
	"github.com/temikus/butter/internal/provider/gemini"
	"github.com/temikus/butter/internal/provider/groq"
	"github.com/temikus/butter/internal/provider/mistral"
	"github.com/temikus/butter/internal/provider/openai"
	"github.com/temikus/butter/internal/provider/openrouter"
	"github.com/temikus/butter/internal/provider/perplexity"
	"github.com/temikus/butter/internal/provider/together"
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
		case "groq":
			registry.Register(groq.New(provCfg.BaseURL, httpClient))
		case "mistral":
			registry.Register(mistral.New(provCfg.BaseURL, httpClient))
		case "together":
			registry.Register(together.New(provCfg.BaseURL, httpClient))
		case "fireworks":
			registry.Register(fireworks.New(provCfg.BaseURL, httpClient))
		case "gemini":
			registry.Register(gemini.New(provCfg.BaseURL, httpClient))
		case "perplexity":
			registry.Register(perplexity.New(provCfg.BaseURL, httpClient))
		default:
			logger.Warn("unknown provider, skipping", "provider", name)
		}
	}

	// Plugin system.
	pluginMgr := plugin.NewManager(logger)

	// Register built-in plugins based on config.
	// Rate limiter runs first so it can reject before other plugins process.
	if _, ok := cfg.Plugins["ratelimit"]; ok {
		pluginMgr.Register(ratelimit.New())
	}
	if _, ok := cfg.Plugins["requestlog"]; ok {
		pluginMgr.Register(requestlog.New(logger))
	}

	var metricsPlugin *metrics.Plugin
	if _, ok := cfg.Plugins["metrics"]; ok {
		metricsPlugin = metrics.New()
		pluginMgr.Register(metricsPlugin)
	}

	if _, ok := cfg.Plugins["tracing"]; ok {
		pluginMgr.Register(tracing.New())
	}

	// Register WASM plugins in config order. Each plugin's user-supplied
	// config is merged into cfg.Plugins so that InitAll can pass it through.
	for _, wpc := range cfg.WASMPlugins {
		wp := pluginwasm.New(wpc.Name, wpc.Path, logger)
		pluginMgr.Register(wp)

		wasmCfg := make(map[string]any, len(wpc.Config))
		for k, v := range wpc.Config {
			wasmCfg[k] = v
		}
		if cfg.Plugins == nil {
			cfg.Plugins = make(map[string]map[string]any)
		}
		cfg.Plugins[wpc.Name] = wasmCfg
	}

	if err := pluginMgr.InitAll(cfg.Plugins); err != nil {
		logger.Error("failed to initialize plugins", "error", err)
		os.Exit(1)
	}

	pluginChain := plugin.NewChain(pluginMgr, logger)

	// Build server options.
	var serverOpts []transport.Option
	if metricsPlugin != nil {
		serverOpts = append(serverOpts, transport.WithMetricsHandler(metricsPlugin.Handler()))
	}
	if cfg.AppKeys.Enabled {
		store := appkey.NewStore()
		for _, entry := range cfg.AppKeys.Keys {
			store.Provision(entry.Key, entry.Label)
		}
		serverOpts = append(serverOpts, transport.WithAppKeyStore(store, cfg.AppKeys.Header, cfg.AppKeys.RequireKey))
		logger.Info("app key tracking enabled",
			"require_key", cfg.AppKeys.RequireKey,
			"header", cfg.AppKeys.Header,
			"pre_provisioned", len(cfg.AppKeys.Keys),
		)
	}

	engine := proxy.NewEngine(registry, cfg, logger, pluginChain)

	// Response cache.
	if cfg.Cache.Enabled {
		var c cache.Cache
		switch cfg.Cache.Backend {
		case "redis":
			rc, err := cache.NewRedis(
				cfg.Cache.Redis.Address,
				cfg.Cache.Redis.Password,
				cfg.Cache.Redis.DB,
				cfg.Cache.Redis.KeyPrefix,
			)
			if err != nil {
				logger.Error("failed to connect to Redis cache", "error", err)
				os.Exit(1)
			}
			defer func() { _ = rc.Close() }()
			c = rc
			logger.Info("response cache enabled",
				"backend", "redis",
				"address", cfg.Cache.Redis.Address,
				"ttl", cfg.Cache.TTL,
			)
		default: // "memory"
			c = cache.NewMemory(cfg.Cache.MaxEntries)
			logger.Info("response cache enabled",
				"backend", "memory",
				"max_entries", cfg.Cache.MaxEntries,
				"ttl", cfg.Cache.TTL,
			)
		}
		engine.SetCache(c, cfg.Cache.TTL)
	}

	server := transport.NewServer(&cfg.Server, engine, logger, pluginChain, serverOpts...)

	// Config hot-reload: poll for file changes and reload routing/keys atomically.
	watcher := config.NewWatcher(*configPath, 5*time.Second, logger, func(newCfg *config.Config) {
		engine.Reload(newCfg)
	})
	watcher.Start()

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
	watcher.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	pluginMgr.CloseAll()
	fmt.Println("butter stopped")
}
