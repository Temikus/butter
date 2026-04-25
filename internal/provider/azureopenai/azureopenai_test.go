package azureopenai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temikus/butter/internal/provider"
)

func TestNew(t *testing.T) {
	p := New("azureopenai", "https://myresource.openai.azure.com/openai/deployments/gpt4o", "2024-10-21", nil)
	if p.Name() != "azureopenai" {
		t.Fatalf("expected name %q, got %q", "azureopenai", p.Name())
	}
}

func TestNewCustomName(t *testing.T) {
	p := New("azureopenai-gpt4o", "https://myresource.openai.azure.com/openai/deployments/gpt4o", "2024-10-21", nil)
	if p.Name() != "azureopenai-gpt4o" {
		t.Fatalf("expected name %q, got %q", "azureopenai-gpt4o", p.Name())
	}
}

func TestSupportsOperations(t *testing.T) {
	p := New("azureopenai", "", "2024-10-21", nil)
	ops := []provider.Operation{
		provider.OpChatCompletion,
		provider.OpChatCompletionStream,
		provider.OpPassthrough,
		provider.OpModels,
		provider.OpEmbeddings,
	}
	for _, op := range ops {
		if !p.SupportsOperation(op) {
			t.Errorf("expected SupportsOperation(%s) = true", op)
		}
	}
}

func TestChatCompletion_AzureHeaders(t *testing.T) {
	var gotAuthHeader string
	var gotAPIVersion string
	var gotBearer string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("api-key")
		gotBearer = r.Header.Get("Authorization")
		gotAPIVersion = r.URL.Query().Get("api-version")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer ts.Close()

	p := New("azureopenai", ts.URL, "2024-10-21", ts.Client())
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "gpt-4o",
		RawBody: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		APIKey:  "test-azure-key",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify api-key header is used (not Bearer).
	if gotAuthHeader != "test-azure-key" {
		t.Errorf("expected api-key header %q, got %q", "test-azure-key", gotAuthHeader)
	}
	if gotBearer != "" {
		t.Errorf("expected no Authorization header, got %q", gotBearer)
	}

	// Verify api-version query param.
	if gotAPIVersion != "2024-10-21" {
		t.Errorf("expected api-version %q, got %q", "2024-10-21", gotAPIVersion)
	}
}

func TestChatCompletionStream_AzureHeaders(t *testing.T) {
	var gotAuthHeader string
	var gotAPIVersion string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("api-key")
		gotAPIVersion = r.URL.Query().Get("api-version")

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer ts.Close()

	p := New("azureopenai", ts.URL, "2024-10-21", ts.Client())
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "gpt-4o",
		Stream:  true,
		RawBody: []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		APIKey:  "test-azure-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if gotAuthHeader != "test-azure-key" {
		t.Errorf("expected api-key header %q, got %q", "test-azure-key", gotAuthHeader)
	}
	if gotAPIVersion != "2024-10-21" {
		t.Errorf("expected api-version %q, got %q", "2024-10-21", gotAPIVersion)
	}

	// Read first chunk.
	chunk, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}
	if !strings.HasPrefix(string(chunk), "data: ") {
		t.Errorf("expected SSE data prefix, got %q", string(chunk))
	}

	// Read DONE marker -> EOF.
	_, err = stream.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF after [DONE], got %v", err)
	}
}

func TestPassthrough_QueryParams(t *testing.T) {
	var gotAPIVersion string
	var gotPath string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIVersion = r.URL.Query().Get("api-version")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	p := New("azureopenai", ts.URL, "2024-10-21", ts.Client())
	resp, err := p.Passthrough(context.Background(), "GET", "/some/path", nil, http.Header{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotPath != "/some/path" {
		t.Errorf("expected path %q, got %q", "/some/path", gotPath)
	}
	if gotAPIVersion != "2024-10-21" {
		t.Errorf("expected api-version %q, got %q", "2024-10-21", gotAPIVersion)
	}
}

func TestSetAuthHeader(t *testing.T) {
	p := New("azureopenai", "", "2024-10-21", nil)

	// Verify it implements AuthHeaderSetter via the interface.
	var setter provider.AuthHeaderSetter = p
	h := http.Header{}
	setter.SetAuthHeader(h, "my-azure-key")

	if h.Get("api-key") != "my-azure-key" {
		t.Errorf("expected api-key %q, got %q", "my-azure-key", h.Get("api-key"))
	}
	if h.Get("Authorization") != "" {
		t.Errorf("expected no Authorization header, got %q", h.Get("Authorization"))
	}
}

func TestEmbeddings_AzureHeaders(t *testing.T) {
	var gotAuthHeader string
	var gotAPIVersion string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("api-key")
		gotAPIVersion = r.URL.Query().Get("api-version")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}]}`))
	}))
	defer ts.Close()

	p := New("azureopenai", ts.URL, "2024-10-21", ts.Client())
	resp, err := p.Embeddings(context.Background(), &provider.EmbeddingRequest{
		RawBody: []byte(`{"model":"text-embedding-ada-002","input":"hello"}`),
		APIKey:  "test-azure-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if gotAuthHeader != "test-azure-key" {
		t.Errorf("expected api-key header %q, got %q", "test-azure-key", gotAuthHeader)
	}
	if gotAPIVersion != "2024-10-21" {
		t.Errorf("expected api-version %q, got %q", "2024-10-21", gotAPIVersion)
	}
}
