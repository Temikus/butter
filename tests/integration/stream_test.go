//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/temikus/butter/internal/appkey"
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

// TestChatCompletions_StreamingUsage verifies that streaming /v1/chat/completions
// requests with an app key record per-model token counts extracted from the
// final usage chunk (enabled via stream_options injection).
func TestChatCompletions_StreamingUsage(t *testing.T) {
	mock := mockOpenAI(t, openAIStreamConditionalUsage)
	cfg := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false)
	butter := cfg.build(t)

	vendResp, err := http.Post(butter.URL+"/v1/app-keys", "application/json", strings.NewReader(`{"label":"stream-svc"}`))
	if err != nil {
		t.Fatalf("vend: %v", err)
	}
	var snap appkey.UsageSnapshot
	if err := json.NewDecoder(vendResp.Body).Decode(&snap); err != nil {
		t.Fatalf("decoding vend response: %v", err)
	}
	vendResp.Body.Close()

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", snap.Key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("streaming /v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, raw)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("draining stream: %v", err)
	}

	usage := waitForUsage(t, butter.URL, snap.Key, func(u *appkey.UsageSnapshot) bool {
		m := u.Models["gpt-4o"]
		return m != nil && m.PromptTokens == 10 && m.CompletionTokens == 5
	})

	if usage.StreamRequests != 1 {
		t.Errorf("expected 1 stream request, got %d", usage.StreamRequests)
	}
	model := usage.Models["gpt-4o"]
	if model.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", model.PromptTokens)
	}
	if model.CompletionTokens != 5 {
		t.Errorf("expected completion_tokens=5, got %d", model.CompletionTokens)
	}
}

// TestChatCompletions_StreamingNoAppKey verifies that without app-key tracking,
// no stream_options injection occurs and the stream completes normally.
func TestChatCompletions_StreamingNoAppKey(t *testing.T) {
	mock := mockOpenAI(t, openAIStreamConditionalUsage)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, raw)
	}

	chunks := collectSSEChunks(t, resp.Body)
	if len(chunks) == 0 {
		t.Fatal("expected SSE chunks")
	}
	if last := chunks[len(chunks)-1]; last != "data: [DONE]" {
		t.Errorf("expected final [DONE], got: %s", last)
	}
	for _, c := range chunks {
		if strings.Contains(c, "prompt_tokens") {
			t.Errorf("unexpected usage in chunk without app key: %s", c)
		}
	}
}
