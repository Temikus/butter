package transport_test

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/provider/openrouter"
	"github.com/temikus/butter/internal/proxy"
	"github.com/temikus/butter/internal/transport"
)

func setupBenchServer(b *testing.B, mockProviderURL string) *httptest.Server {
	b.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				BaseURL: mockProviderURL,
				Keys:    []config.KeyConfig{{Key: "bench-key", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "openrouter",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProviderURL, nil))

	engine := proxy.NewEngine(registry, cfg, logger)
	srv := transport.NewServer(&cfg.Server, engine, logger)

	return httptest.NewServer(srv.Handler())
}

func BenchmarkNonStreamingRequest(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"bench","choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer mockProv.Close()

	ts := setupBenchServer(b, mockProv.URL)
	defer ts.Close()

	reqBody := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
		if err != nil {
			b.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkStreamingRequest(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"chunk\":2}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockProv.Close()

	ts := setupBenchServer(b, mockProv.URL)
	defer ts.Close()

	reqBody := `{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true}`

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
		if err != nil {
			b.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
}

func BenchmarkIsStreamRequest(b *testing.B) {
	bodies := [][]byte{
		[]byte(`{"model":"test","messages":[],"stream":true}`),
		[]byte(`{"model":"test","messages":[],"stream": true}`),
		[]byte(`{"model":"test","messages":[]}`),
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		body := bodies[i%len(bodies)]
		// Replicate the isStreamRequest logic since it's unexported.
		_ = strings.Contains(string(body), `"stream":true`) ||
			strings.Contains(string(body), `"stream": true`)
	}
}
