//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestModels_WithRoutes(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withModel("gpt-4o", "openai").
		withModel("text-embedding-3-small", "openai").
		build(t)

	resp, err := http.Get(butter.URL + "/v1/models")
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
	if !ok {
		t.Fatalf("expected data array, got: %v", result)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 models, got %d", len(data))
	}

	// Verify each model has required fields.
	for _, m := range data {
		model, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("expected model object, got: %v", m)
		}
		if model["object"] != "model" {
			t.Errorf("expected object=model, got %v", model["object"])
		}
		if model["id"] == nil || model["id"] == "" {
			t.Errorf("expected non-empty id")
		}
		if model["owned_by"] != "openai" {
			t.Errorf("expected owned_by=openai, got %v", model["owned_by"])
		}
	}
}

func TestModels_NoRoutes(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Get(butter.URL + "/v1/models")
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
	data, ok := result["data"].([]any)
	if !ok {
		t.Fatalf("expected data array, got: %v", result)
	}
	if len(data) != 0 {
		t.Errorf("expected 0 models when no routes configured, got %d", len(data))
	}
}

func TestModels_ContentType(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Get(butter.URL + "/v1/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", ct)
	}
}
