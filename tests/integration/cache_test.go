//go:build integration

package integration

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Cacheable request: no temperature field (treated as 0), non-streaming.
const reqCacheable = `{"model":"gpt-4o","messages":[{"role":"user","content":"cached question"}]}`

func TestCache_HitOnIdenticalRequest(t *testing.T) {
	var calls atomic.Int32
	mock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		openAISuccess(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withCache().
		build(t)

	// First request — cache miss, provider is called.
	resp1, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqCacheable))
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", resp1.StatusCode)
	}

	// Second identical request — cache hit, provider should NOT be called again.
	resp2, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqCacheable))
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second request: expected 200, got %d", resp2.StatusCode)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("expected provider called once (cache hit on second), got %d", got)
	}
	if resp2.Header.Get("X-Butter-Cache") != "hit" {
		t.Errorf("expected X-Butter-Cache: hit on second response, got %q", resp2.Header.Get("X-Butter-Cache"))
	}
}

func TestCache_MissOnDifferentMessages(t *testing.T) {
	var calls atomic.Int32
	mock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		openAISuccess(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withCache().
		build(t)

	req1 := `{"model":"gpt-4o","messages":[{"role":"user","content":"question one"}]}`
	req2 := `{"model":"gpt-4o","messages":[{"role":"user","content":"question two"}]}`

	for i, req := range []string{req1, req2} {
		resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, resp.StatusCode)
		}
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("expected provider called twice (different messages), got %d", got)
	}
}

func TestCache_StreamingRequestsNotCached(t *testing.T) {
	var calls atomic.Int32
	mock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		openAIStream(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withCache().
		build(t)

	req := `{"model":"gpt-4o","messages":[{"role":"user","content":"cached question"}],"stream":true}`

	for i := range 2 {
		resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
		resp.Body.Close()
	}

	// Both streaming requests must reach the provider (streaming is never cached).
	if got := calls.Load(); got != 2 {
		t.Errorf("expected provider called twice (streaming not cached), got %d", got)
	}
}

func TestCache_NonZeroTemperatureNotCached(t *testing.T) {
	var calls atomic.Int32
	mock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		openAISuccess(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withCache().
		build(t)

	req := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`

	for i := range 2 {
		resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
		resp.Body.Close()
	}

	// Temperature > 0 → non-deterministic, must not be cached.
	if got := calls.Load(); got != 2 {
		t.Errorf("expected provider called twice (non-zero temperature), got %d", got)
	}
}
