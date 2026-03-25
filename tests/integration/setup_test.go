//go:build integration

package integration

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temikus/butter/internal/cache"
	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/provider/anthropic"
	"github.com/temikus/butter/internal/provider/openai"
	"github.com/temikus/butter/internal/provider/openrouter"
	"github.com/temikus/butter/internal/proxy"
	"github.com/temikus/butter/internal/transport"
)

// ─── Butter Server Builder ───────────────────────────────────────────────────

// serverCfg holds options for building a Butter test server.
// Provider base URLs point at mock httptest servers, not real APIs.
type serverCfg struct {
	providers    map[string]string   // provider name → mock base URL
	defaultProv  string
	modelRoutes  map[string][]string // model → ordered provider list
	failover     bool
	cacheEnabled bool
}

func newServerCfg() *serverCfg {
	return &serverCfg{
		providers:   make(map[string]string),
		modelRoutes: make(map[string][]string),
	}
}

func (c *serverCfg) withProvider(name, baseURL string) *serverCfg {
	c.providers[name] = baseURL
	return c
}

func (c *serverCfg) withDefault(name string) *serverCfg {
	c.defaultProv = name
	return c
}

func (c *serverCfg) withModel(model string, providers ...string) *serverCfg {
	c.modelRoutes[model] = providers
	return c
}

func (c *serverCfg) withFailover() *serverCfg {
	c.failover = true
	return c
}

func (c *serverCfg) withCache() *serverCfg {
	c.cacheEnabled = true
	return c
}

// build starts a Butter httptest.Server wired to the configured mock providers.
// The server is closed automatically when the test ends.
func (c *serverCfg) build(t *testing.T) *httptest.Server {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	httpClient := &http.Client{Timeout: 5 * time.Second}
	registry := provider.NewRegistry()

	provCfgs := make(map[string]config.ProviderConfig)
	for name, baseURL := range c.providers {
		provCfgs[name] = config.ProviderConfig{
			BaseURL: baseURL,
			Keys:    []config.KeyConfig{{Key: "test-key", Weight: 1}},
		}
		switch name {
		case "openai":
			registry.Register(openai.New(baseURL, httpClient))
		case "openrouter":
			registry.Register(openrouter.New(baseURL, httpClient))
		case "anthropic":
			registry.Register(anthropic.New(baseURL, httpClient))
		}
	}

	models := make(map[string]config.ModelRoute)
	for model, provs := range c.modelRoutes {
		models[model] = config.ModelRoute{Providers: provs, Strategy: "priority"}
	}

	cfg := &config.Config{
		Server: config.ServerConfig{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
		Providers: provCfgs,
		Routing: config.RoutingConfig{
			DefaultProvider: c.defaultProv,
			Models:          models,
			Failover: config.FailoverConfig{
				Enabled:    c.failover,
				MaxRetries: 2,
				Backoff: config.BackoffConfig{
					Initial:    1 * time.Millisecond,
					Multiplier: 2.0,
					Max:        5 * time.Millisecond,
				},
				RetryOn: []int{429, 500, 502, 503, 504},
			},
		},
	}

	mgr := plugin.NewManager(logger)
	chain := plugin.NewChain(mgr, logger)
	engine := proxy.NewEngine(registry, cfg, logger, chain)

	if c.cacheEnabled {
		engine.SetCache(cache.NewMemory(100), 5*time.Minute)
	}

	srv := transport.NewServer(&cfg.Server, engine, logger, chain)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// ─── OpenAI-Compatible Mock Servers ─────────────────────────────────────────
//
// openaicompat providers append "/chat/completions" to their base URL, so
// mocks register handlers at "/chat/completions" (not "/v1/chat/completions").

// mockOpenAI returns a mock server for OpenAI-compatible providers.
// handler may be nil to use the default success response.
func mockOpenAI(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	if handler == nil {
		handler = openAISuccess
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/completions", handler)
	// Catch-all for passthrough tests
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// mockCountingOpenAI returns a mock server that counts handler invocations.
func mockCountingOpenAI(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	wrapped := func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		handler(w, r)
	}
	return mockOpenAI(t, wrapped), &count
}

func openAISuccess(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{
		"id": "chatcmpl-test",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)
}

func openAIStream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f := w.(http.Flusher)
	for _, chunk := range []string{
		`data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-s","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	} {
		_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
		f.Flush()
	}
}

// errorHandler returns a handler that always responds with the given status.
func errorHandler(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"mock error","code":%d}}`, code)
	}
}

// ─── Anthropic Mock Server ───────────────────────────────────────────────────
//
// The anthropic provider appends "/messages" to its base URL.

func mockAnthropic(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	if handler == nil {
		handler = anthropicSuccess
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /messages", handler)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func anthropicSuccess(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)
}

func anthropicStream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f := w.(http.Flusher)
	for _, event := range []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-3-5-sonnet-20241022\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello!\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	} {
		_, _ = fmt.Fprintf(w, "%s\n\n", event)
		f.Flush()
	}
}
