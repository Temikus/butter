package transport_test

import (
	"bufio"
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
	"github.com/temikus/butter/internal/provider/anthropic"
	"github.com/temikus/butter/internal/provider/openrouter"
	"github.com/temikus/butter/internal/proxy"
	"github.com/temikus/butter/internal/transport"
)

// streamWriteTimeout is deliberately shorter than the total time the mock
// upstream takes to emit all of its chunks, so a stream that relies on the
// one-shot WriteTimeout deadline is severed part-way through.
const (
	streamWriteTimeout = 250 * time.Millisecond
	chunkInterval      = 100 * time.Millisecond
	dribbleChunks      = 6 // 6 * 100ms = 600ms > 250ms WriteTimeout
)

// newHardenedTestServer starts a real http.Server (not just the handler) with
// the given WriteTimeout, since WriteTimeout has no effect on a bare handler.
func newHardenedTestServer(t *testing.T, cfg *config.Config, reg *provider.Registry) *httptest.Server {
	t.Helper()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	engine := proxy.NewEngine(reg, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil)

	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.WriteTimeout = cfg.Server.WriteTimeout
	ts.Config.ReadTimeout = cfg.Server.ReadTimeout
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// dribbleSSE writes n SSE events spaced chunkInterval apart, using the given
// formatter, so the response outlives the server's WriteTimeout.
func dribbleSSE(w http.ResponseWriter, n int, format func(i int) string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for i := 0; i < n; i++ {
		time.Sleep(chunkInterval)
		_, _ = fmt.Fprint(w, format(i))
		flusher.Flush()
	}
}

// readSSELines reads the response body line by line until EOF, returning the
// lines seen and any read error. A severed connection surfaces here as a
// non-EOF error (unexpected EOF / connection reset) after partial output.
func readSSELines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, sc.Err()
}

// TestChatStreamOutlivesWriteTimeout is the regression test for the SSE /
// WriteTimeout coupling: a completion that streams for longer than
// WriteTimeout must still deliver every chunk.
func TestChatStreamOutlivesWriteTimeout(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dribbleSSE(w, dribbleChunks, func(i int) string {
			return fmt.Sprintf("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"chunk-%d\"},\"index\":0}]}\n\n", i)
		})
	}))
	defer mockProvider.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: streamWriteTimeout,
		},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				BaseURL: mockProvider.URL,
				Keys:    []config.KeyConfig{{Key: "test-key", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{DefaultProvider: "openrouter"},
	}
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProvider.URL, nil))

	ts := newHardenedTestServer(t, cfg, registry)

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}],"stream":true}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	lines, err := readSSELines(resp.Body)
	if err != nil {
		t.Fatalf("stream severed mid-flight after %d lines: %v", len(lines), err)
	}

	got := strings.Join(lines, "\n")
	for i := 0; i < dribbleChunks; i++ {
		want := fmt.Sprintf("chunk-%d", i)
		if !strings.Contains(got, want) {
			t.Errorf("missing %s; stream ended early:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "[DONE]") {
		t.Errorf("missing [DONE] marker:\n%s", got)
	}
}

// TestAnthropicStreamOutlivesWriteTimeout covers the /v1/messages relay, which
// streams through io.Copy rather than the chunk loop.
func TestAnthropicStreamOutlivesWriteTimeout(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dribbleSSE(w, dribbleChunks, func(i int) string {
			return fmt.Sprintf("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"chunk-%d\"}}\n\n", i)
		})
	}))
	defer mockProvider.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: streamWriteTimeout,
		},
		Providers: map[string]config.ProviderConfig{
			"anthropic": {
				BaseURL:        mockProvider.URL,
				CredentialMode: "passthrough",
			},
		},
		Routing: config.RoutingConfig{DefaultProvider: "anthropic"},
	}
	registry := provider.NewRegistry()
	registry.Register(anthropic.New(mockProvider.URL, nil))

	ts := newHardenedTestServer(t, cfg, registry)

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-3","stream":true,"messages":[{"role":"user","content":"Hi"}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	lines, err := readSSELines(resp.Body)
	if err != nil {
		t.Fatalf("stream severed mid-flight after %d lines: %v", len(lines), err)
	}

	got := strings.Join(lines, "\n")
	for i := 0; i < dribbleChunks; i++ {
		want := fmt.Sprintf("chunk-%d", i)
		if !strings.Contains(got, want) {
			t.Errorf("missing %s; stream ended early:\n%s", want, got)
		}
	}
}

// TestNativePassthroughStreamOutlivesWriteTimeout covers the raw
// /native/{provider}/* SSE relay.
func TestNativePassthroughStreamOutlivesWriteTimeout(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dribbleSSE(w, dribbleChunks, func(i int) string {
			return fmt.Sprintf("data: chunk-%d\n\n", i)
		})
	}))
	defer mockProvider.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: streamWriteTimeout,
		},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				BaseURL: mockProvider.URL,
				Keys:    []config.KeyConfig{{Key: "test-key", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{DefaultProvider: "openrouter"},
	}
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProvider.URL, nil))

	ts := newHardenedTestServer(t, cfg, registry)

	resp, err := http.Get(ts.URL + "/native/openrouter/stream")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	lines, err := readSSELines(resp.Body)
	if err != nil {
		t.Fatalf("stream severed mid-flight after %d lines: %v", len(lines), err)
	}

	got := strings.Join(lines, "\n")
	for i := 0; i < dribbleChunks; i++ {
		want := fmt.Sprintf("chunk-%d", i)
		if !strings.Contains(got, want) {
			t.Errorf("missing %s; stream ended early:\n%s", want, got)
		}
	}
}
