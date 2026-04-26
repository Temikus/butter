package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/temikus/butter/internal/appkey"
	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/provider/anthropic"
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

func TestNativePassthroughStreamingSSE(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)

		events := []string{
			"data: {\"type\":\"chunk\",\"index\":1}\n\n",
			"data: {\"type\":\"chunk\",\"index\":2}\n\n",
			"data: {\"type\":\"chunk\",\"index\":3}\n\n",
		}
		for _, event := range events {
			_, _ = fmt.Fprint(w, event)
			flusher.Flush()
		}
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/native/openrouter/v1/messages", "application/json",
		strings.NewReader(`{"model":"test","stream":true}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	full := string(body)
	for i := 1; i <= 3; i++ {
		expected := fmt.Sprintf(`"index":%d`, i)
		if !strings.Contains(full, expected) {
			t.Errorf("missing chunk with index %d in response: %s", i, full)
		}
	}
}

func TestNativePassthroughNonStreamingUnchanged(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"result":"ok","data":[1,2,3]}`)
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/native/openrouter/some/endpoint")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	expected := `{"result":"ok","data":[1,2,3]}`
	if string(body) != expected {
		t.Errorf("expected %q, got %q", expected, body)
	}
}

func TestNativePassthroughSSEContentTypeWithCharset(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"msg\":\"hello\"}\n\n")
		flusher.Flush()
	}))
	defer mockProvider.Close()

	ts := setupTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/native/openrouter/stream-endpoint")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("expected response to contain 'hello', got: %s", body)
	}
}

func setupAnthropicTestServer(t *testing.T, mockProviderURL string) *httptest.Server {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Providers: map[string]config.ProviderConfig{
			"anthropic": {
				BaseURL:        mockProviderURL,
				CredentialMode: "passthrough",
			},
		},
		Routing: config.RoutingConfig{
			DefaultProvider: "anthropic",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(anthropic.New(mockProviderURL, nil))

	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil)
	ts := httptest.NewServer(srv.Handler())
	return ts
}

func TestAnthropicMessages_NonStreaming(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.Error(w, "not found", 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_123","type":"message","content":[{"type":"text","text":"Hello"}]}`)
	}))
	defer mockProvider.Close()

	ts := setupAnthropicTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-3","messages":[{"role":"user","content":"Hi"}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "msg_123") {
		t.Errorf("expected response to contain msg_123, got: %s", body)
	}
}

func TestAnthropicMessages_Streaming(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}
		for _, e := range events {
			_, _ = fmt.Fprint(w, e)
			flusher.Flush()
		}
	}))
	defer mockProvider.Close()

	ts := setupAnthropicTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-3","stream":true,"messages":[{"role":"user","content":"Hi"}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "message_stop") {
		t.Errorf("expected SSE events, got: %s", body)
	}
}

func TestAnthropicMessages_UsageTracking(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_42","type":"message","model":"claude-3","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":11,"output_tokens":7}}`)
	}))
	defer mockProvider.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{Address: ":0", ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second},
		Providers: map[string]config.ProviderConfig{
			"anthropic": {BaseURL: mockProvider.URL, CredentialMode: "passthrough"},
		},
		Routing: config.RoutingConfig{DefaultProvider: "anthropic"},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(anthropic.New(mockProvider.URL, nil))

	store := appkey.NewStore()
	const key = "btr_msgsusage0000000000a"
	store.Provision(key, "test")

	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil,
		transport.WithAppKeyStore(store, "X-Butter-App-Key", false))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/messages", strings.NewReader(
		`{"model":"claude-3","messages":[{"role":"user","content":"Hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "msg_42") {
		t.Errorf("response body lost: %s", body)
	}

	// Wait for async usage recording.
	deadline := time.Now().Add(time.Second)
	var snap *appkey.UsageSnapshot
	for time.Now().Before(deadline) {
		snap = store.Lookup(key).Snapshot()
		if snap.TotalRequests > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap == nil || snap.TotalRequests != 1 {
		t.Fatalf("expected total_requests=1, got snapshot=%+v", snap)
	}
	if snap.NonStreamRequests != 1 {
		t.Errorf("expected non_stream_requests=1, got %d", snap.NonStreamRequests)
	}
	model, ok := snap.Models["claude-3"]
	if !ok {
		t.Fatalf("expected per-model entry for claude-3, got %v", snap.Models)
	}
	if model.PromptTokens != 11 {
		t.Errorf("expected prompt_tokens=11 (input_tokens), got %d", model.PromptTokens)
	}
	if model.CompletionTokens != 7 {
		t.Errorf("expected completion_tokens=7 (output_tokens), got %d", model.CompletionTokens)
	}
}

func TestAnthropicMessages_StreamingUsageTracking(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer mockProvider.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{Address: ":0", ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second},
		Providers: map[string]config.ProviderConfig{
			"anthropic": {BaseURL: mockProvider.URL, CredentialMode: "passthrough"},
		},
		Routing: config.RoutingConfig{DefaultProvider: "anthropic"},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(anthropic.New(mockProvider.URL, nil))

	store := appkey.NewStore()
	const key = "btr_msgsstream000000000z"
	store.Provision(key, "test")

	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil,
		transport.WithAppKeyStore(store, "X-Butter-App-Key", false))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/messages", strings.NewReader(
		`{"model":"claude-3","stream":true,"messages":[{"role":"user","content":"Hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	deadline := time.Now().Add(time.Second)
	var snap *appkey.UsageSnapshot
	for time.Now().Before(deadline) {
		snap = store.Lookup(key).Snapshot()
		if snap.TotalRequests > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap == nil || snap.TotalRequests != 1 {
		t.Fatalf("expected total_requests=1, got snapshot=%+v", snap)
	}
	if snap.StreamRequests != 1 {
		t.Errorf("expected stream_requests=1, got %d", snap.StreamRequests)
	}
	model, ok := snap.Models["claude-3"]
	if !ok {
		t.Fatalf("expected per-model entry for claude-3, got %v", snap.Models)
	}
	if model.Requests != 1 {
		t.Errorf("expected model.requests=1, got %d", model.Requests)
	}
	if model.PromptTokens != 0 || model.CompletionTokens != 0 {
		t.Errorf("expected zero tokens for streaming (parity), got pt=%d ct=%d", model.PromptTokens, model.CompletionTokens)
	}
}

func TestAnthropicMessages_ProviderError(t *testing.T) {
	mockProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"too many requests"}}`)
	}))
	defer mockProvider.Close()

	ts := setupAnthropicTestServer(t, mockProvider.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-3","messages":[]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 429 {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
}

// capturingHandler is an slog.Handler that records log records for inspection.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	level   slog.Level
}

func (h *capturingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }

func TestHealthzLogsAtDebug(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mockProv.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				BaseURL: mockProv.URL,
				Keys:    []config.KeyConfig{{Key: "test-key", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{DefaultProvider: "openrouter"},
	}

	handler := &capturingHandler{level: slog.LevelDebug}
	logger := slog.New(handler)
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProv.URL, nil))
	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	handler.mu.Lock()
	records := handler.records
	handler.mu.Unlock()

	var found *slog.Record
	for i := range records {
		if records[i].Message == "request completed" {
			found = &records[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected 'request completed' log record, got none")
	}
	if found.Level != slog.LevelDebug {
		t.Errorf("expected Debug level for /healthz log, got %v", found.Level)
	}
}

func TestNonHealthzLogsAtInfo(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","index":0}]}`)
	}))
	defer mockProv.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Address:      ":0",
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {
				BaseURL: mockProv.URL,
				Keys:    []config.KeyConfig{{Key: "test-key", Weight: 1}},
			},
		},
		Routing: config.RoutingConfig{DefaultProvider: "openrouter"},
	}

	handler := &capturingHandler{level: slog.LevelDebug}
	logger := slog.New(handler)
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProv.URL, nil))
	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	handler.mu.Lock()
	records := handler.records
	handler.mu.Unlock()

	var found *slog.Record
	for i := range records {
		if records[i].Message == "request completed" {
			found = &records[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected 'request completed' log record, got none")
	}
	if found.Level != slog.LevelInfo {
		t.Errorf("expected Info level for /v1/chat/completions log, got %v", found.Level)
	}
}

// captureMetadataPlugin is a TransportPlugin that captures metadata from PreHTTP.
type captureMetadataPlugin struct {
	mu       sync.Mutex
	metadata map[string]any
}

func (p *captureMetadataPlugin) Name() string              { return "capture-metadata" }
func (p *captureMetadataPlugin) Init(_ map[string]any) error { return nil }
func (p *captureMetadataPlugin) Close() error              { return nil }
func (p *captureMetadataPlugin) PostHTTP(_ *plugin.RequestContext) error { return nil }
func (p *captureMetadataPlugin) StreamChunk(_ *plugin.RequestContext, chunk []byte) ([]byte, error) {
	return chunk, nil
}

func (p *captureMetadataPlugin) PreHTTP(ctx *plugin.RequestContext) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metadata = make(map[string]any)
	for k, v := range ctx.Metadata {
		p.metadata[k] = v
	}
	return nil
}

func (p *captureMetadataPlugin) getMetadata() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make(map[string]any)
	for k, v := range p.metadata {
		cp[k] = v
	}
	return cp
}

// setupAppKeyTestServer creates a test server with app-key tracking and a
// metadata-capturing plugin. Returns the httptest.Server and the capture plugin.
func setupAppKeyTestServer(t *testing.T, mockProviderURL string, provisionKeys ...string) (*httptest.Server, *captureMetadataPlugin) {
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
		Routing: config.RoutingConfig{DefaultProvider: "openrouter"},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockProviderURL, nil))

	capture := &captureMetadataPlugin{}
	mgr := plugin.NewManager(logger)
	mgr.Register(capture)
	chain := plugin.NewChain(mgr, logger)

	store := appkey.NewStore()
	for _, k := range provisionKeys {
		store.Provision(k, "test")
	}

	engine := proxy.NewEngine(registry, cfg, logger, chain)
	srv := transport.NewServer(&cfg.Server, engine, logger, chain,
		transport.WithAppKeyStore(store, "X-Butter-App-Key", false))
	ts := httptest.NewServer(srv.Handler())
	return ts, capture
}

func TestAppKeyMetadataInjected(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","index":0}]}`)
	}))
	defer mockProv.Close()

	ts, capture := setupAppKeyTestServer(t, mockProv.URL, "btr_test0000000000000000")
	defer ts.Close()

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", "btr_test0000000000000000")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	meta := capture.getMetadata()
	if meta["app_key"] != "btr_test0000000000000000" {
		t.Errorf("expected app_key=btr_test0000000000000000 in metadata, got %v", meta["app_key"])
	}
}

func TestAppKeyMetadataAbsentWhenNoKey(t *testing.T) {
	mockProv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","index":0}]}`)
	}))
	defer mockProv.Close()

	ts, capture := setupAppKeyTestServer(t, mockProv.URL)
	defer ts.Close()

	reqBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	meta := capture.getMetadata()
	if _, exists := meta["app_key"]; exists {
		t.Errorf("expected no app_key in metadata when header is absent, got %v", meta["app_key"])
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
