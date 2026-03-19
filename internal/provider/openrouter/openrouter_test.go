package openrouter

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
	p := New("", nil)
	if p.Name() != "openrouter" {
		t.Errorf("expected openrouter, got %s", p.Name())
	}
}

func TestSupportsOperation(t *testing.T) {
	p := New("", nil)

	tests := []struct {
		op   provider.Operation
		want bool
	}{
		{provider.OpChatCompletion, true},
		{provider.OpPassthrough, true},
		{provider.OpModels, true},
		{provider.OpEmbeddings, false},
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
		fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
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

		fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: {\"chunk\":2}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := New(server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "test",
		Stream:  true,
		RawBody: []byte(`{"model":"test","stream":true}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stream.Close()

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
		fmt.Fprint(w, `{"error":"rate limited"}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	_, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "test",
		Stream:  true,
		RawBody: []byte(`{"model":"test","stream":true}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}

	pe, ok := err.(*providerError)
	if !ok {
		t.Fatalf("expected *providerError, got %T", err)
	}
	if pe.Response.StatusCode != 429 {
		t.Errorf("expected status 429, got %d", pe.Response.StatusCode)
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
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	headers := http.Header{"X-Custom": []string{"value"}}
	resp, err := p.Passthrough(context.Background(), "GET", "/models", nil, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"data":[]}` {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestChatCompletionNetworkError(t *testing.T) {
	// Use a URL that will refuse connections.
	p := New("http://127.0.0.1:1", nil)
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
		fmt.Fprint(w, `{"error":"internal server error"}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 500 {
		t.Errorf("expected 500, got %d", resp.StatusCode)
	}
	if string(resp.RawBody) != `{"error":"internal server error"}` {
		t.Errorf("unexpected body: %s", resp.RawBody)
	}
}

func TestStreamMalformedSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		// Comment line (starts with colon)
		fmt.Fprint(w, ": this is a comment\n\n")
		flusher.Flush()
		// Event line
		fmt.Fprint(w, "event: message\n\n")
		flusher.Flush()
		// Normal data
		fmt.Fprint(w, "data: {\"chunk\":1}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := New(server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "test",
		Stream:  true,
		RawBody: []byte(`{"model":"test","stream":true}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer stream.Close()

	// Should get the comment line (contains ":"), then event line, then data chunk.
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
		// Block until context is cancelled.
		<-r.Context().Done()
	}))
	defer server.Close()

	p := New(server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

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
		fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "test",
		RawBody: []byte(`{"model":"test"}`),
		APIKey:  "", // Empty key.
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	// Pass URL with trailing slash.
	p := New(server.URL+"/", nil)
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
