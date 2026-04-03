//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const reqEmbedding = `{"model":"text-embedding-3-small","input":"hello world"}`

func TestEmbeddings_OpenAIProvider(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/embeddings", "application/json", strings.NewReader(reqEmbedding))
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
	if result["object"] != "list" {
		t.Errorf("expected object=list, got %v", result["object"])
	}
	data, ok := result["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("expected data array in response, got: %v", result)
	}
}

func TestEmbeddings_UnsupportedProvider(t *testing.T) {
	mock := mockAnthropic(t, nil)
	butter := newServerCfg().
		withProvider("anthropic", mock.URL).
		withDefault("anthropic").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/embeddings", "application/json", strings.NewReader(reqEmbedding))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected error for unsupported provider, got 200")
	}
}

func TestEmbeddings_MissingModel(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/embeddings", "application/json", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Fatal("expected error for missing model, got 200")
	}
}

func TestEmbeddings_ModelRouting(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withModel("text-embedding-3-small", "openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/embeddings", "application/json", strings.NewReader(reqEmbedding))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}
