package openaicompat

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/temikus/butter/internal/provider"
)

func TestProviderName(t *testing.T) {
	p := New("testprovider", "http://localhost", nil)
	if p.Name() != "testprovider" {
		t.Errorf("expected testprovider, got %s", p.Name())
	}
}

func TestSupportsOperation(t *testing.T) {
	p := New("test", "http://localhost", nil)

	tests := []struct {
		op   provider.Operation
		want bool
	}{
		{provider.OpChatCompletion, true},
		{provider.OpChatCompletionStream, true},
		{provider.OpPassthrough, true},
		{provider.OpModels, true},
		{provider.OpEmbeddings, true},
	}

	for _, tt := range tests {
		if got := p.SupportsOperation(tt.op); got != tt.want {
			t.Errorf("SupportsOperation(%s) = %v, want %v", tt.op, got, tt.want)
		}
	}
}

func TestChatCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}

		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"test"}` {
			t.Errorf("unexpected body: %s", body)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if string(resp.RawBody) != `{"id":"resp-1"}` {
		t.Errorf("unexpected body: %s", resp.RawBody)
	}
}

func TestChatCompletionStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer server.Close()

	p := New("test", server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "test",
		Stream:  true,
		RawBody: []byte(`{"model":"test","stream":true}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	chunk1, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 1: %v", err)
	}
	if string(chunk1) != `data: {"chunk":1}` {
		t.Errorf("unexpected chunk 1: %s", chunk1)
	}

	chunk2, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 2: %v", err)
	}
	if string(chunk2) != `data: {"chunk":2}` {
		t.Errorf("unexpected chunk 2: %s", chunk2)
	}

	_, err = stream.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF after [DONE], got: %v", err)
	}
}

func TestChatCompletionStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"error":"rate limited"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	_, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "test",
		Stream:  true,
		RawBody: []byte(`{"model":"test","stream":true}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}

	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 429 {
		t.Errorf("expected status 429, got %d", pe.StatusCode)
	}
}

func TestPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("expected /models, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Custom") != "value" {
			t.Errorf("expected X-Custom header")
		}
		_, _ = fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	headers := http.Header{"X-Custom": []string{"value"}}
	resp, err := p.Passthrough(context.Background(), "GET", "/models", nil, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"data":[]}` {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestChatCompletionNetworkError(t *testing.T) {
	p := New("test", "http://127.0.0.1:1", nil)
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for unreachable URL")
	}
}

func TestChatCompletionNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"error":"internal server error"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 500 {
		t.Errorf("expected status 500, got %d", pe.StatusCode)
	}
	if pe.Message != `{"error":"internal server error"}` {
		t.Errorf("unexpected message: %s", pe.Message)
	}
}

func TestStreamMalformedSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		_, _ = fmt.Fprint(w, ": this is a comment\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: message\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "test",
		Stream:  true,
		RawBody: []byte(`{"model":"test","stream":true}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	chunk1, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 1: %v", err)
	}
	if string(chunk1) != ": this is a comment" {
		t.Errorf("unexpected chunk 1: %q", chunk1)
	}

	chunk2, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 2: %v", err)
	}
	if string(chunk2) != "event: message" {
		t.Errorf("unexpected chunk 2: %q", chunk2)
	}

	chunk3, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 3: %v", err)
	}
	if string(chunk3) != `data: {"chunk":1}` {
		t.Errorf("unexpected chunk 3: %q", chunk3)
	}

	_, err = stream.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF after [DONE], got: %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.ChatCompletion(ctx, &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestBuildRequestNoAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected no Authorization header, got %s", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestEmbeddings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("expected /embeddings, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}]}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	resp, err := p.Embeddings(context.Background(), &provider.EmbeddingRequest{
		Model:   "text-embedding-3-small",
		RawBody: []byte(`{"model":"text-embedding-3-small","input":"hello"}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if string(resp.RawBody) != `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}]}` {
		t.Errorf("unexpected body: %s", resp.RawBody)
	}
}

func TestEmbeddingsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil)
	_, err := p.Embeddings(context.Background(), &provider.EmbeddingRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test","input":"hello"}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 400 {
		t.Errorf("expected status 400, got %d", pe.StatusCode)
	}
}

func TestNewWithOptions(t *testing.T) {
	p := New("test", "http://localhost", nil,
		WithAuthHeaderName("x-custom-key"),
		WithQueryParams(map[string]string{"version": "v1"}),
	)
	if p.Name() != "test" {
		t.Errorf("expected name test, got %s", p.Name())
	}
	if p.opts.authHeaderName != "x-custom-key" {
		t.Errorf("expected authHeaderName x-custom-key, got %s", p.opts.authHeaderName)
	}
	if p.opts.queryParams["version"] != "v1" {
		t.Errorf("expected queryParams[version]=v1, got %s", p.opts.queryParams["version"])
	}
}

func TestBuildRequestCustomAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-custom-key") != "my-key" {
			t.Errorf("expected x-custom-key header, got %q", r.Header.Get("x-custom-key"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil, WithAuthHeaderName("x-custom-key"))
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "my-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRequestQueryParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api-version") != "2024-10-21" {
			t.Errorf("expected api-version=2024-10-21, got %q", r.URL.Query().Get("api-version"))
		}
		if r.URL.Query().Get("extra") != "value" {
			t.Errorf("expected extra=value, got %q", r.URL.Query().Get("extra"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil, WithQueryParams(map[string]string{
		"api-version": "2024-10-21",
		"extra":       "value",
	}))
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSetAuthHeaderDefault(t *testing.T) {
	p := New("test", "http://localhost", nil)
	h := http.Header{}
	p.SetAuthHeader(h, "my-key")

	if h.Get("Authorization") != "Bearer my-key" {
		t.Errorf("expected Authorization Bearer, got %q", h.Get("Authorization"))
	}
}

func TestSetAuthHeaderCustom(t *testing.T) {
	p := New("test", "http://localhost", nil, WithAuthHeaderName("api-key"))
	h := http.Header{}
	p.SetAuthHeader(h, "my-key")

	if h.Get("api-key") != "my-key" {
		t.Errorf("expected api-key my-key, got %q", h.Get("api-key"))
	}
	if h.Get("Authorization") != "" {
		t.Errorf("expected no Authorization header, got %q", h.Get("Authorization"))
	}
}

func TestPassthroughQueryParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api-version") != "v1" {
			t.Errorf("expected api-version=v1, got %q", r.URL.Query().Get("api-version"))
		}
		if r.URL.Path != "/models" {
			t.Errorf("expected /models, got %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	p := New("test", server.URL, nil, WithQueryParams(map[string]string{"api-version": "v1"}))
	resp, err := p.Passthrough(context.Background(), "GET", "/models", nil, http.Header{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
}

func TestBaseURLTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New("test", server.URL+"/", nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
