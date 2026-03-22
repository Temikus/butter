package transport_test

import (
	"encoding/json"
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

func setupTestServer(t *testing.T, mockProviderURL string) *httptest.Server {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				BaseURL: mockProviderURL,
				Keys:    []config.KeyConfig{{Key: "test-key", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "openrouter",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProviderURL, nil))

	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil)

	// Use httptest to wrap the handler
	ts := httptest.NewServer(srv.Handler())
	return ts
}

func TestChatCompletionNonStreaming(t *testing.T) {
	// Mock provider that returns a canned response.
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected auth header, got: %s", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "chatcmpl-test123",
			"object": "chat.completion",
			"model": "gpt-4o-mini",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello from mock!"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`)
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if result["id"] != "chatcmpl-test123" {
		t.Errorf("unexpected response id: %v", result["id"])
	}
}

func TestChatCompletionStreaming(t *testing.T) {
	// Mock provider that returns SSE chunks.
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		chunks := []string{
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"role":"assistant"},"index":0}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hello"},"index":0}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" world"},"index":0}]}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	reqBody := `{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hi"}], "stream": true}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}

	content := string(body)
	if !strings.Contains(content, "Hello") {
		t.Errorf("stream missing 'Hello' chunk: %s", content)
	}
	if !strings.Contains(content, "[DONE]") {
		t.Errorf("stream missing [DONE] marker: %s", content)
	}
}

func TestHealthz(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMissingModel(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	reqBody := `{"messages": [{"role": "user", "content": "Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestProviderError502(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"error":"internal"}`)
	}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	reqBody := `{"model": "test", "messages": [{"role": "user", "content": "Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Non-streaming: provider 500 is relayed as-is (not wrapped in 502).
	if resp.StatusCode != 500 {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
}

func TestStreamDispatchError(t *testing.T) {
	// Provider that returns 500 for streaming requests.
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"error":"stream fail"}`)
	}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	reqBody := `{"model": "test", "messages": [{"role": "user", "content": "Hi"}], "stream": true}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The upstream returned 500, which is forwarded as the status code via ProviderError.
	if resp.StatusCode != 500 {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	errObj, ok := result["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got: %v", result)
	}
	if errObj["type"] != "proxy_error" {
		t.Errorf("expected proxy_error type, got: %v", errObj["type"])
	}
}

func TestInvalidJSONBody(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{not valid json`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestEmptyBody(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestWrongHTTPMethod(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

func TestProviderNon200Relayed(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "preserved")
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"error":"rate limited"}`)
	}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	reqBody := `{"model": "test", "messages": [{"role": "user", "content": "Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}

	// The error is now wrapped in a proxy_error envelope via ProviderError.
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	errObj, ok := result["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got: %v", result)
	}
	if errObj["type"] != "proxy_error" {
		t.Errorf("expected proxy_error type, got: %v", errObj["type"])
	}
}

func TestNativePassthroughGET(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected auth header, got: %s", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"gpt-4o"}]}`)
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/native/openrouter/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "gpt-4o") {
		t.Errorf("expected response to contain gpt-4o, got: %s", body)
	}
}

func TestNativePassthroughPOST(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Echo back the body to verify it was forwarded.
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	reqBody := `{"input":"hello","model":"text-embedding-3-small"}`
	resp, err := http.Post(ts.URL+"/native/openrouter/embeddings", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != reqBody {
		t.Errorf("expected echoed body %q, got %q", reqBody, body)
	}
}

func TestNativePassthroughNestedPath(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Echo the path so the test can verify correct forwarding.
		_, _ = fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/native/openrouter/chat/completions")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if result["path"] != "/chat/completions" {
		t.Errorf("expected /chat/completions, got %s", result["path"])
	}
}

func TestNativePassthroughUnknownProvider(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/native/nonexistent/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestNativePassthroughRelaysUpstreamHeaders(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Provider", "test-value")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/native/openrouter/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if v := resp.Header.Get("X-Custom-Provider"); v != "test-value" {
		t.Errorf("expected X-Custom-Provider=test-value, got %q", v)
	}
}

func TestConcurrentRequests(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"ok"}`)
	}))
	defer mockProv.Close()

	ts := setupTestServer(t, mockProv.URL)
	defer ts.Close()

	const concurrent = 50
	errs := make(chan error, concurrent)

	for i := 0; i < concurrent; i++ {
		go func() {
			reqBody := `{"model": "test", "messages": [{"role": "user", "content": "Hi"}]}`
			resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("expected 200, got %d", resp.StatusCode)
				return
			}
			errs <- nil
		}()
	}

	for i := 0; i < concurrent; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent request %d failed: %v", i, err)
		}
	}
}
