package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/provider"
)

type mockProvider struct {
	name     string
	response *provider.ChatResponse
	chatFn   func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error)
	streamFn func(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error)
	lastReq  *provider.ChatRequest
}

func (m *mockProvider) Name() string                                 { return m.name }
func (m *mockProvider) SupportsOperation(op provider.Operation) bool { return true }
func (m *mockProvider) ChatCompletion(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
	m.lastReq = req
	if m.chatFn != nil {
		return m.chatFn(ctx, req)
	}
	return m.response, nil
}
func (m *mockProvider) ChatCompletionStream(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
	m.lastReq = req
	if m.streamFn != nil {
		return m.streamFn(ctx, req)
	}
	return nil, nil
}
func (m *mockProvider) Passthrough(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	return nil, nil
}

// mockStream implements provider.Stream for testing.
type mockStream struct {
	chunks [][]byte
	idx    int
}

func (s *mockStream) Next() ([]byte, error) {
	if s.idx >= len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.idx]
	s.idx++
	return chunk, nil
}

func (s *mockStream) Close() error { return nil }

func newTestEngine(providers ...provider.Provider) *Engine {
	reg := provider.NewRegistry()
	for _, p := range providers {
		reg.Register(p)
	}

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				Keys: []config.KeyConfig{{Key: "sk-test", Weight: 1}},
			},
			"openai": {
				Keys: []config.KeyConfig{{Key: "sk-openai", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "openrouter",
			Models: map[string]config.ModelRoute{
				"gpt-4o": {Providers: []string{"openai"}, Strategy: "priority"},
			},
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewEngine(reg, cfg, logger, nil)
}

func TestDispatchDefaultProvider(t *testing.T) {
	mock := &mockProvider{
		name: "openrouter",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"test"}`),
			StatusCode: 200,
		},
	}
	engine := newTestEngine(mock)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"some-model","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDispatchModelRoute(t *testing.T) {
	openrouterMock := &mockProvider{
		name: "openrouter",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"openrouter"}`),
			StatusCode: 200,
		},
	}
	openaiMock := &mockProvider{
		name: "openai",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"openai"}`),
			StatusCode: 200,
		},
	}
	engine := newTestEngine(openrouterMock, openaiMock)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"gpt-4o","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.RawBody) != `{"id":"openai"}` {
		t.Errorf("expected openai response, got: %s", resp.RawBody)
	}
}

func TestDispatchExplicitProvider(t *testing.T) {
	openrouterMock := &mockProvider{
		name: "openrouter",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"openrouter"}`),
			StatusCode: 200,
		},
	}
	openaiMock := &mockProvider{
		name: "openai",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"openai"}`),
			StatusCode: 200,
		},
	}
	engine := newTestEngine(openrouterMock, openaiMock)

	// Explicitly request openrouter even though gpt-4o routes to openai
	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"gpt-4o","messages":[],"provider":"openrouter"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.RawBody) != `{"id":"openrouter"}` {
		t.Errorf("expected openrouter response, got: %s", resp.RawBody)
	}
}

func TestDispatchMissingModel(t *testing.T) {
	engine := newTestEngine(&mockProvider{name: "openrouter"})

	_, err := engine.Dispatch(context.Background(), []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestDispatchInvalidJSON(t *testing.T) {
	engine := newTestEngine(&mockProvider{name: "openrouter"})

	_, err := engine.Dispatch(context.Background(), []byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestDispatchUnknownProvider(t *testing.T) {
	engine := newTestEngine(&mockProvider{name: "openrouter"})

	_, err := engine.Dispatch(context.Background(), []byte(`{"model":"x","provider":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestDispatchStreamDefaultProvider(t *testing.T) {
	mock := &mockProvider{
		name: "openrouter",
		streamFn: func(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
			return &mockStream{chunks: [][]byte{
				[]byte(`data: {"chunk":1}`),
				[]byte(`data: {"chunk":2}`),
			}}, nil
		},
	}
	engine := newTestEngine(mock)

	stream, err := engine.DispatchStream(context.Background(), []byte(`{"model":"some-model","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	chunk, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(chunk) != `data: {"chunk":1}` {
		t.Errorf("unexpected chunk: %s", chunk)
	}

	chunk, err = stream.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(chunk) != `data: {"chunk":2}` {
		t.Errorf("unexpected chunk: %s", chunk)
	}

	_, err = stream.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got: %v", err)
	}
}

func TestDispatchStreamModelRoute(t *testing.T) {
	openrouterMock := &mockProvider{
		name: "openrouter",
		streamFn: func(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
			return &mockStream{chunks: [][]byte{[]byte(`data: openrouter`)}}, nil
		},
	}
	openaiMock := &mockProvider{
		name: "openai",
		streamFn: func(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
			return &mockStream{chunks: [][]byte{[]byte(`data: openai`)}}, nil
		},
	}
	engine := newTestEngine(openrouterMock, openaiMock)

	stream, err := engine.DispatchStream(context.Background(), []byte(`{"model":"gpt-4o","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	chunk, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(chunk) != `data: openai` {
		t.Errorf("expected openai stream, got: %s", chunk)
	}
}

func TestDispatchNoProviderConfigured(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "openrouter"})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {Keys: []config.KeyConfig{{Key: "sk-test", Weight: 1}}},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "", // No default.
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := NewEngine(reg, cfg, logger, nil)

	_, err := engine.Dispatch(context.Background(), []byte(`{"model":"unknown-model","messages":[]}`))
	if err == nil {
		t.Fatal("expected error for model with no route and no default provider")
	}
}

func TestDispatchContextCancelled(t *testing.T) {
	mock := &mockProvider{
		name: "openrouter",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			return nil, ctx.Err()
		},
	}
	engine := newTestEngine(mock)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := engine.Dispatch(ctx, []byte(`{"model":"test","messages":[]}`))
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestSelectKeyEmpty(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{
		name: "empty-keys",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"test"}`),
			StatusCode: 200,
		},
	})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"empty-keys": {Keys: []config.KeyConfig{}}, // No keys.
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "empty-keys",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := NewEngine(reg, cfg, logger, nil)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"test","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still succeed — provider gets empty API key.
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// --- Phase 2: Weighted Key Selection Tests ---

func TestSelectKeyWeighted(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "test-provider"})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"test-provider": {Keys: []config.KeyConfig{
				{Key: "sk-heavy", Weight: 8},
				{Key: "sk-light", Weight: 2},
			}},
		},
		Routing: config.RoutingConfig{DefaultProvider: "test-provider"},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := NewEngine(reg, cfg, logger, nil)

	counts := map[string]int{}
	const iterations = 10000
	for i := 0; i < iterations; i++ {
		key := engine.selectKey("test-provider", "any-model")
		counts[key]++
	}

	// With weights 8:2, sk-heavy should get ~80% of selections.
	heavyRatio := float64(counts["sk-heavy"]) / float64(iterations)
	if math.Abs(heavyRatio-0.8) > 0.05 {
		t.Errorf("expected sk-heavy ratio ~0.80, got %.2f (heavy=%d, light=%d)",
			heavyRatio, counts["sk-heavy"], counts["sk-light"])
	}
}

func TestSelectKeyModelFilter(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "test-provider"})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"test-provider": {Keys: []config.KeyConfig{
				{Key: "sk-gpt4-only", Weight: 1, Models: []string{"gpt-4o"}},
				{Key: "sk-general", Weight: 1},
			}},
		},
		Routing: config.RoutingConfig{DefaultProvider: "test-provider"},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := NewEngine(reg, cfg, logger, nil)

	// For gpt-4o, both keys are eligible.
	gpt4Keys := map[string]bool{}
	for i := 0; i < 100; i++ {
		gpt4Keys[engine.selectKey("test-provider", "gpt-4o")] = true
	}
	if !gpt4Keys["sk-gpt4-only"] || !gpt4Keys["sk-general"] {
		t.Errorf("expected both keys for gpt-4o, got: %v", gpt4Keys)
	}

	// For claude-3, only sk-general is eligible (sk-gpt4-only has Models filter).
	for i := 0; i < 100; i++ {
		key := engine.selectKey("test-provider", "claude-3")
		if key != "sk-general" {
			t.Fatalf("expected sk-general for claude-3, got %s", key)
		}
	}
}

// --- Phase 2: Failover/Retry Tests ---

func newFailoverEngine(failover config.FailoverConfig, providers ...provider.Provider) *Engine {
	reg := provider.NewRegistry()
	providerConfigs := make(map[string]config.ProviderConfig)
	for _, p := range providers {
		reg.Register(p)
		providerConfigs[p.Name()] = config.ProviderConfig{
			Keys: []config.KeyConfig{{Key: "sk-" + p.Name(), Weight: 1}},
		}
	}

	cfg := &config.Config{
		Providers: providerConfigs,
		Routing: config.RoutingConfig{
			DefaultProvider: providers[0].Name(),
			Models: map[string]config.ModelRoute{
				"test-model": {Providers: func() []string {
					names := make([]string, len(providers))
					for i, p := range providers {
						names[i] = p.Name()
					}
					return names
				}()},
			},
			Failover: failover,
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	e := NewEngine(reg, cfg, logger, nil)
	e.sleepFn = func(d time.Duration) {} // no-op sleep in tests
	return e
}

func TestFailoverRetryOnStatus(t *testing.T) {
	var calls atomic.Int32
	mock := &mockProvider{
		name: "primary",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			n := calls.Add(1)
			if n <= 2 {
				return nil, &provider.ProviderError{StatusCode: 429, Message: "rate limited"}
			}
			return &provider.ChatResponse{RawBody: []byte(`{"ok":true}`), StatusCode: 200}, nil
		},
	}

	engine := newFailoverEngine(config.FailoverConfig{
		Enabled:    true,
		MaxRetries: 3,
		RetryOn:    []int{429},
		Backoff:    config.BackoffConfig{Initial: time.Millisecond, Multiplier: 2, Max: time.Second},
	}, mock)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"test-model","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.RawBody) != `{"ok":true}` {
		t.Errorf("unexpected response: %s", resp.RawBody)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}

func TestFailoverNextProvider(t *testing.T) {
	primary := &mockProvider{
		name: "primary",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			return nil, &provider.ProviderError{StatusCode: 500, Message: "internal error"}
		},
	}
	secondary := &mockProvider{
		name: "secondary",
		response: &provider.ChatResponse{RawBody: []byte(`{"from":"secondary"}`), StatusCode: 200},
	}

	engine := newFailoverEngine(config.FailoverConfig{
		Enabled:    true,
		MaxRetries: 2,
		RetryOn:    []int{429}, // 500 is NOT retryable — should fall through to next provider
		Backoff:    config.BackoffConfig{Initial: time.Millisecond, Multiplier: 2, Max: time.Second},
	}, primary, secondary)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"test-model","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.RawBody) != `{"from":"secondary"}` {
		t.Errorf("expected secondary response, got: %s", resp.RawBody)
	}
}

func TestFailoverDisabled(t *testing.T) {
	var calls atomic.Int32
	mock := &mockProvider{
		name: "primary",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			calls.Add(1)
			return nil, &provider.ProviderError{StatusCode: 429, Message: "rate limited"}
		},
	}

	engine := newFailoverEngine(config.FailoverConfig{
		Enabled: false,
	}, mock)

	_, err := engine.Dispatch(context.Background(), []byte(`{"model":"test-model","messages":[]}`))
	if err == nil {
		t.Fatal("expected error when failover disabled")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 call (no retry), got %d", got)
	}
}

func TestFailoverExhausted(t *testing.T) {
	primary := &mockProvider{
		name: "primary",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			return nil, &provider.ProviderError{StatusCode: 429, Message: "rate limited"}
		},
	}
	secondary := &mockProvider{
		name: "secondary",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			return nil, &provider.ProviderError{StatusCode: 429, Message: "also rate limited"}
		},
	}

	engine := newFailoverEngine(config.FailoverConfig{
		Enabled:    true,
		MaxRetries: 1,
		RetryOn:    []int{429},
		Backoff:    config.BackoffConfig{Initial: time.Millisecond, Multiplier: 2, Max: time.Second},
	}, primary, secondary)

	_, err := engine.Dispatch(context.Background(), []byte(`{"model":"test-model","messages":[]}`))
	if err == nil {
		t.Fatal("expected error when all providers exhausted")
	}

	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if pe.Message != "also rate limited" {
		t.Errorf("expected last error from secondary, got: %s", pe.Message)
	}
}

func TestFailoverStreamRetry(t *testing.T) {
	var calls atomic.Int32
	mock := &mockProvider{
		name: "primary",
		streamFn: func(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
			n := calls.Add(1)
			if n <= 1 {
				return nil, &provider.ProviderError{StatusCode: 429, Message: "rate limited"}
			}
			return &mockStream{chunks: [][]byte{[]byte(`data: {"ok":true}`)}}, nil
		},
	}

	engine := newFailoverEngine(config.FailoverConfig{
		Enabled:    true,
		MaxRetries: 2,
		RetryOn:    []int{429},
		Backoff:    config.BackoffConfig{Initial: time.Millisecond, Multiplier: 2, Max: time.Second},
	}, mock)

	stream, err := engine.DispatchStream(context.Background(), []byte(`{"model":"test-model","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	chunk, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading stream: %v", err)
	}
	if string(chunk) != `data: {"ok":true}` {
		t.Errorf("unexpected chunk: %s", chunk)
	}
}

func TestFailoverNonProviderError(t *testing.T) {
	// Non-ProviderError errors (e.g. network errors) should not be retried —
	// they break out of the retry loop and try the next provider.
	primary := &mockProvider{
		name: "primary",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}
	secondary := &mockProvider{
		name: "secondary",
		response: &provider.ChatResponse{RawBody: []byte(`{"from":"secondary"}`), StatusCode: 200},
	}

	engine := newFailoverEngine(config.FailoverConfig{
		Enabled:    true,
		MaxRetries: 2,
		RetryOn:    []int{429, 500},
		Backoff:    config.BackoffConfig{Initial: time.Millisecond, Multiplier: 2, Max: time.Second},
	}, primary, secondary)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"test-model","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.RawBody) != `{"from":"secondary"}` {
		t.Errorf("expected secondary response, got: %s", resp.RawBody)
	}
}
