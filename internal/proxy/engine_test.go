package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

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
	return NewEngine(reg, cfg, logger)
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
	defer stream.Close()

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
	defer stream.Close()

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
	engine := NewEngine(reg, cfg, logger)

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
	engine := NewEngine(reg, cfg, logger)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"test","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still succeed — provider gets empty API key.
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSelectKeyReturnsFirst(t *testing.T) {
	mock := &mockProvider{
		name: "openrouter",
		chatFn: func(ctx context.Context, req *provider.ChatRequest) (*provider.ChatResponse, error) {
			return &provider.ChatResponse{
				RawBody:    []byte(`{"key":"` + req.APIKey + `"}`),
				StatusCode: 200,
			}, nil
		},
	}

	reg := provider.NewRegistry()
	reg.Register(mock)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {Keys: []config.KeyConfig{
				{Key: "sk-first", Weight: 1},
				{Key: "sk-second", Weight: 5},
				{Key: "sk-third", Weight: 1},
			}},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "openrouter",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := NewEngine(reg, cfg, logger)

	resp, err := engine.Dispatch(context.Background(), []byte(`{"model":"test","messages":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.RawBody) != `{"key":"sk-first"}` {
		t.Errorf("expected first key, got: %s", resp.RawBody)
	}
}
