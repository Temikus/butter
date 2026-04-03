package gemini

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temikus/butter/internal/provider"
)

func TestProviderName(t *testing.T) {
	p := New("", nil)
	if p.Name() != "gemini" {
		t.Errorf("expected gemini, got %s", p.Name())
	}
}

func TestSupportsOperation(t *testing.T) {
	p := New("", nil)

	tests := []struct {
		op   provider.Operation
		want bool
	}{
		{provider.OpChatCompletion, true},
		{provider.OpChatCompletionStream, true},
		{provider.OpPassthrough, true},
		{provider.OpEmbeddings, false},
		{provider.OpModels, false},
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
		if !strings.Contains(r.URL.Path, "generateContent") {
			t.Errorf("expected path to contain generateContent, got %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "gemini-2.0-flash") {
			t.Errorf("expected path to contain model name, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("expected key=test-key query param, got %s", r.URL.RawQuery)
		}

		// Verify request body is translated.
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"contents"`) {
			t.Errorf("expected Gemini format with 'contents', got: %s", body)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, `{
			"candidates": [{
				"content": {"parts": [{"text": "Hello!"}], "role": "model"},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
		}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "gemini-2.0-flash",
		RawBody: []byte(`{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hello"}]}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(resp.RawBody), `"chat.completion"`) {
		t.Errorf("expected OpenAI format response, got: %s", resp.RawBody)
	}
	if !strings.Contains(string(resp.RawBody), `"Hello!"`) {
		t.Errorf("expected 'Hello!' in response, got: %s", resp.RawBody)
	}
}

func TestChatCompletion_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = fmt.Fprint(w, `{"error":{"message":"invalid model"}}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "bad-model",
		RawBody: []byte(`{"model":"bad-model","messages":[{"role":"user","content":"hi"}]}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 400 {
		t.Errorf("expected 400, got %d", pe.StatusCode)
	}
}

func TestChatCompletionStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("expected streamGenerateContent in path, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("expected alt=sse query param, got %s", r.URL.RawQuery)
		}

		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		chunks := []string{
			`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"text":"!"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "gemini-2.0-flash",
		Stream:  true,
		RawBody: []byte(`{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// First chunk.
	chunk1, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 1: %v", err)
	}
	if !strings.Contains(string(chunk1), "Hello") {
		t.Errorf("expected chunk 1 to contain 'Hello', got: %s", chunk1)
	}
	if !strings.HasPrefix(string(chunk1), "data: ") {
		t.Errorf("expected chunk to start with 'data: ', got: %s", chunk1)
	}

	// Second chunk (with finish reason).
	chunk2, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error reading chunk 2: %v", err)
	}
	if !strings.Contains(string(chunk2), `"stop"`) {
		t.Errorf("expected chunk 2 to contain finish_reason 'stop', got: %s", chunk2)
	}

	// Should get EOF after all chunks.
	_, err = stream.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF after all chunks, got: %v", err)
	}
}

func TestChatCompletionStream_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	_, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "gemini-2.0-flash",
		Stream:  true,
		RawBody: []byte(`{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 429 {
		t.Errorf("expected 429, got %d", pe.StatusCode)
	}
}

func TestBuildRequest_URLConstruction(t *testing.T) {
	p := New("https://example.com", nil)

	// Non-streaming.
	req, err := p.buildRequest(context.Background(), "gemini-2.0-flash", false, []byte("{}"), "my-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.URL.Path != "/v1beta/models/gemini-2.0-flash:generateContent" {
		t.Errorf("unexpected path: %s", req.URL.Path)
	}
	if req.URL.Query().Get("key") != "my-key" {
		t.Errorf("expected key=my-key, got %s", req.URL.RawQuery)
	}
	if req.URL.Query().Get("alt") != "" {
		t.Error("non-streaming should not have alt=sse")
	}

	// Streaming.
	req, err = p.buildRequest(context.Background(), "gemini-2.0-flash", true, []byte("{}"), "my-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.URL.Path != "/v1beta/models/gemini-2.0-flash:streamGenerateContent" {
		t.Errorf("unexpected path: %s", req.URL.Path)
	}
	if req.URL.Query().Get("alt") != "sse" {
		t.Errorf("streaming should have alt=sse, got %s", req.URL.RawQuery)
	}
	if req.URL.Query().Get("key") != "my-key" {
		t.Errorf("expected key=my-key, got %s", req.URL.RawQuery)
	}
}
