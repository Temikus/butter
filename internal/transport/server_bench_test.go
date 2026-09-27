package transport_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	return setupBenchServerWithClient(b, mockProviderURL, nil)
}

func setupBenchServerWithClient(b *testing.B, mockProviderURL string, client *http.Client) *httptest.Server {
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
	registry.Register(openrouter.New(mockProviderURL, client))

	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil)

	return httptest.NewServer(srv.Handler())
}

func BenchmarkNonStreamingRequest(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"bench","choices":[{"message":{"content":"hi"}}]}`)
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
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

func BenchmarkStreamingRequest(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"chunk\":2}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
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
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

func BenchmarkBaselineNonStreaming(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"bench","choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer mockProv.Close()

	reqBody := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(mockProv.URL, "application/json", strings.NewReader(reqBody))
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

func BenchmarkBaselineStreaming(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"chunk\":2}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockProv.Close()

	reqBody := `{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true}`

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(mockProv.URL, "application/json", strings.NewReader(reqBody))
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkBaselineStdlibProxy is the floor for a stdlib net/http proxy: the
// same client → proxy → upstream hops as BenchmarkNonStreamingRequest with no
// Butter logic. Butter's own cost is the difference between the two.
func BenchmarkBaselineStdlibProxy(b *testing.B) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"bench","choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer mockProv.Close()

	upstream := &http.Client{}
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, _ := http.NewRequestWithContext(r.Context(), "POST", mockProv.URL+"/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := upstream.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
	}))
	defer proxySrv.Close()

	reqBody := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
}

// BenchmarkParallelNonStreaming measures overhead under concurrent load against
// an upstream with fixed latency, using the production upstream client. The
// overhead metrics subtract the upstream delay from observed latency.
func BenchmarkParallelNonStreaming(b *testing.B) {
	const upstreamDelay = 5 * time.Millisecond

	var conns atomic.Int64
	mockProv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(upstreamDelay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"bench","choices":[{"message":{"content":"hi"}}]}`)
	}))
	mockProv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	mockProv.Start()
	defer mockProv.Close()

	upstream := provider.NewHTTPClient(30 * time.Second)
	defer upstream.CloseIdleConnections()
	ts := setupBenchServerWithClient(b, mockProv.URL, upstream)
	defer ts.Close()

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4096}}
	defer client.CloseIdleConnections()
	reqBody := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`

	var (
		mu   sync.Mutex
		lats []time.Duration
	)
	b.SetParallelism(16) // 16*GOMAXPROCS in flight; macOS listen backlog is 128
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		local := make([]time.Duration, 0, 1024)
		for pb.Next() {
			start := time.Now()
			resp, err := client.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			local = append(local, time.Since(start))
		}
		mu.Lock()
		lats = append(lats, local...)
		mu.Unlock()
	})
	b.StopTimer()

	slices.Sort(lats)
	if n := len(lats); n > 0 {
		b.ReportMetric(float64((lats[n/2] - upstreamDelay).Microseconds()), "p50-overhead-us")
		b.ReportMetric(float64((lats[n*99/100] - upstreamDelay).Microseconds()), "p99-overhead-us")
	}
	b.ReportMetric(float64(conns.Load()), "upstream-conns")
}
