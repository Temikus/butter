//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestFailover_RetryOnRetryableStatus verifies that Butter retries the same
// provider when it returns a retryable status (503). The third attempt succeeds.
func TestFailover_RetryOnRetryableStatus(t *testing.T) {
	var calls atomic.Int32
	mock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			errorHandler(http.StatusServiceUnavailable)(w, r)
		} else {
			openAISuccess(w, r)
		}
	})

	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withFailover(). // enables retries (maxRetries=2 → 3 attempts total)
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after retry, got %d: %s", resp.StatusCode, body)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("expected 3 provider calls (2 failures + 1 success), got %d", n)
	}
}

// TestFailover_FallsBackToSecondProvider verifies that when the primary provider
// exhausts all retries, Butter falls back to the secondary provider.
func TestFailover_FallsBackToSecondProvider(t *testing.T) {
	var primaryCalls, secondaryCalls atomic.Int32

	// Primary always returns 503.
	primaryMock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		errorHandler(http.StatusServiceUnavailable)(w, r)
	})
	// Secondary succeeds.
	secondaryMock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		secondaryCalls.Add(1)
		openAISuccess(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", primaryMock.URL).
		withProvider("openrouter", secondaryMock.URL).
		withModel("gpt-4o", "openai", "openrouter"). // priority order
		withFailover().                               // maxRetries=2 → 3 primary attempts
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 from secondary, got %d: %s", resp.StatusCode, body)
	}
	// Primary should have been tried maxRetries+1 = 3 times before giving up.
	if n := primaryCalls.Load(); n != 3 {
		t.Errorf("expected primary to be called 3 times, got %d", n)
	}
	if n := secondaryCalls.Load(); n != 1 {
		t.Errorf("expected secondary to be called once, got %d", n)
	}
}

// TestFailover_NonRetryableSkipsImmediately verifies that a non-retryable error
// (400) causes Butter to skip directly to the next provider without retrying.
func TestFailover_NonRetryableSkipsImmediately(t *testing.T) {
	var primaryCalls, secondaryCalls atomic.Int32

	primaryMock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		errorHandler(http.StatusBadRequest)(w, r) // 400 is not in retry_on list
	})
	secondaryMock := mockOpenAI(t, func(w http.ResponseWriter, r *http.Request) {
		secondaryCalls.Add(1)
		openAISuccess(w, r)
	})

	butter := newServerCfg().
		withProvider("openai", primaryMock.URL).
		withProvider("openrouter", secondaryMock.URL).
		withModel("gpt-4o", "openai", "openrouter").
		withFailover().
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 from secondary, got %d: %s", resp.StatusCode, body)
	}
	// Primary called once only (no retries for non-retryable errors).
	if n := primaryCalls.Load(); n != 1 {
		t.Errorf("expected primary called once (no retry on 400), got %d", n)
	}
}

// TestFailover_AllProvidersFail returns the last error when all providers fail.
func TestFailover_AllProvidersFail(t *testing.T) {
	mock := mockOpenAI(t, errorHandler(http.StatusBadGateway))

	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withFailover().
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 {
		t.Errorf("expected error status when all providers fail, got %d", resp.StatusCode)
	}
}
