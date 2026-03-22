package proxy

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/provider"
)

func newBenchEngine() *Engine {
	mock := &mockProvider{
		name: "openrouter",
		response: &provider.ChatResponse{
			RawBody:    []byte(`{"id":"bench","choices":[{"message":{"content":"hi"}}]}`),
			StatusCode: 200,
		},
		streamFn: func(ctx context.Context, req *provider.ChatRequest) (provider.Stream, error) {
			return &mockStream{chunks: [][]byte{
				[]byte(`data: {"chunk":1}`),
				[]byte(`data: {"chunk":2}`),
			}}, nil
		},
	}

	reg := provider.NewRegistry()
	reg.Register(mock)

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openrouter": {Keys: []config.KeyConfig{{Key: "sk-bench", Weight: 1}}},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "openrouter",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return NewEngine(reg, cfg, logger, nil)
}

var benchBody = []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)
var benchStreamBody = []byte(`{"model":"test-model","messages":[{"role":"user","content":"hello"}],"stream":true}`)

func BenchmarkDispatch(b *testing.B) {
	engine := newBenchEngine()
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := engine.Dispatch(ctx, benchBody)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDispatchStream(b *testing.B) {
	engine := newBenchEngine()
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		stream, err := engine.DispatchStream(ctx, benchStreamBody)
		if err != nil {
			b.Fatal(err)
		}
		// Drain the stream.
		for {
			_, err := stream.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		_ = stream.Close()
	}
}

func BenchmarkParseAndRoute(b *testing.B) {
	engine := newBenchEngine()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _, err := engine.parseAndRoute(benchBody)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSelectKeyWeighted(b *testing.B) {
	reg := provider.NewRegistry()
	reg.Register(&mockProvider{name: "bench-provider"})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"bench-provider": {Keys: []config.KeyConfig{
				{Key: "sk-1", Weight: 5},
				{Key: "sk-2", Weight: 3},
				{Key: "sk-3", Weight: 2},
			}},
		},
		Routing: config.RoutingConfig{DefaultProvider: "bench-provider"},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := NewEngine(reg, cfg, logger, nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		engine.selectKey("bench-provider", "any-model")
	}
}
