//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	reqGPT4o  = `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`
	reqClaude = `{"model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"hello"}],"max_tokens":1024}`
)

func TestChat_OpenAIProvider(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices in response, got: %v", result)
	}
}

func TestChat_OpenRouterProvider(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openrouter", mock.URL).
		withDefault("openrouter").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestChat_AnthropicTranslation(t *testing.T) {
	mock := mockAnthropic(t, nil)
	butter := newServerCfg().
		withProvider("anthropic", mock.URL).
		withDefault("anthropic").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqClaude))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Verify the Anthropic response was translated to OpenAI chat completion format.
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	choices, ok := result["choices"].([]any)
	if !ok || len(choices) == 0 {
		t.Fatalf("expected choices after translation, got: %v", result)
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Hello!" {
		t.Errorf("expected translated content 'Hello!', got: %v", msg["content"])
	}
}

func TestChat_ModelBasedRouting(t *testing.T) {
	var openaiCalls, anthropicCalls atomic.Int32

	openaiMock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		openaiCalls.Add(1)
		openAISuccess(w, r)
	})
	anthropicMock := mockAnthropic(t, func(w http.ResponseWriter, r *http.Request) {
		anthropicCalls.Add(1)
		anthropicSuccess(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", openaiMock.URL).
		withProvider("anthropic", anthropicMock.URL).
		withModel("gpt-4o", "openai").
		withModel("claude-3-5-sonnet-20241022", "anthropic").
		build(t)

	// GPT-4o should route to openai mock.
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("gpt-4o request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("gpt-4o: expected 200, got %d", resp.StatusCode)
	}

	// Claude should route to anthropic mock.
	resp2, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqClaude))
	if err != nil {
		t.Fatalf("claude request failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("claude: expected 200, got %d", resp2.StatusCode)
	}

	if openaiCalls.Load() != 1 {
		t.Errorf("expected openai to be called once, got %d", openaiCalls.Load())
	}
	if anthropicCalls.Load() != 1 {
		t.Errorf("expected anthropic to be called once, got %d", anthropicCalls.Load())
	}
}

func TestChat_ExplicitProviderOverride(t *testing.T) {
	mock := mockOpenAI(t, nil)
	// No default provider set — relies entirely on the "provider" field in the request.
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		build(t)

	req := `{"model":"gpt-4o","provider":"openai","messages":[{"role":"user","content":"hello"}]}`
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestChat_MissingModel_Returns4xx(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Errorf("expected 4xx for missing model, got %d", resp.StatusCode)
	}
}

func TestChat_ProviderError_PropagatesStatus(t *testing.T) {
	mock := mockOpenAI(t, errorHandler(http.StatusTooManyRequests))
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	// Butter should propagate the provider's error status.
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 from provider error, got %d", resp.StatusCode)
	}
}
