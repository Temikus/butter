package mistral

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
	if p.Name() != "mistral" {
		t.Errorf("expected mistral, got %s", p.Name())
	}
}

func TestDefaultBaseURL(t *testing.T) {
	p := New("", nil)
	if p.Name() != "mistral" {
		t.Errorf("expected mistral, got %s", p.Name())
	}
}

func TestCustomBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp-1"}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "mistral-large-latest",
		RawBody: []byte(`{"model":"mistral-large-latest"}`),
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
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := New(server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "mistral-large-latest",
		Stream:  true,
		RawBody: []byte(`{"model":"mistral-large-latest","stream":true}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	chunk, err := stream.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(chunk) != `data: {"chunk":1}` {
		t.Errorf("unexpected chunk: %s", chunk)
	}

	_, err = stream.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF after [DONE], got: %v", err)
	}
}
