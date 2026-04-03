//go:build integration

package integration

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestStream_OpenAIFormat(t *testing.T) {
	mock := mockOpenAI(t, openAIStream)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	req := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true}`
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type: text/event-stream, got: %s", ct)
	}

	chunks := collectSSEChunks(t, resp.Body)

	if len(chunks) == 0 {
		t.Fatal("expected at least one SSE data chunk")
	}
	if last := chunks[len(chunks)-1]; last != "data: [DONE]" {
		t.Errorf("expected final chunk 'data: [DONE]', got: %s", last)
	}
	if !containsContent(chunks, "Hello") {
		t.Errorf("expected 'Hello' in stream output, got chunks: %v", chunks)
	}
}

func TestStream_AnthropicTranslation(t *testing.T) {
	mock := mockAnthropic(t, anthropicStream)
	butter := newServerCfg().
		withProvider("anthropic", mock.URL).
		withDefault("anthropic").
		build(t)

	req := `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hello"}],"max_tokens":1024,"stream":true}`
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	chunks := collectSSEChunks(t, resp.Body)

	if len(chunks) == 0 {
		t.Fatal("expected at least one translated SSE chunk")
	}
	if last := chunks[len(chunks)-1]; last != "data: [DONE]" {
		t.Errorf("expected final chunk 'data: [DONE]', got: %s", last)
	}
	// Anthropic stream should be translated to OpenAI format with "Hello!" content.
	if !containsContent(chunks, "Hello") {
		t.Errorf("expected translated 'Hello' content in stream, got chunks: %v", chunks)
	}
}

func TestStream_GeminiTranslation(t *testing.T) {
	mock := mockGemini(t, geminiStream)
	butter := newServerCfg().
		withProvider("gemini", mock.URL).
		withDefault("gemini").
		build(t)

	req := `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	chunks := collectSSEChunks(t, resp.Body)

	if len(chunks) == 0 {
		t.Fatal("expected at least one translated SSE chunk")
	}
	// Gemini stream should be translated to OpenAI format.
	if !containsContent(chunks, "Hello") {
		t.Errorf("expected translated 'Hello' content in stream, got chunks: %v", chunks)
	}
	if !containsContent(chunks, "Gemini") {
		t.Errorf("expected 'Gemini' content in stream, got chunks: %v", chunks)
	}
}

// collectSSEChunks reads an SSE response body and returns all "data: ..." lines.
func collectSSEChunks(t *testing.T, body io.Reader) []string {
	t.Helper()
	var chunks []string
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			chunks = append(chunks, line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanning SSE response: %v", err)
	}
	return chunks
}

// containsContent checks whether any chunk contains the given string.
func containsContent(chunks []string, s string) bool {
	for _, c := range chunks {
		if strings.Contains(c, s) {
			return true
		}
	}
	return false
}
