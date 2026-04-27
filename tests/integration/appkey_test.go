//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/temikus/butter/internal/appkey"
)

func TestAppKey_VendAndUse(t *testing.T) {
	mock := mockOpenAI(t, nil)
	cfg := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false)
	butter := cfg.build(t)

	// Vend a key.
	resp, err := http.Post(butter.URL+"/v1/app-keys", "application/json", strings.NewReader(`{"label":"test-svc"}`))
	if err != nil {
		t.Fatalf("POST /v1/app-keys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var snap appkey.UsageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decoding vend response: %v", err)
	}
	if !appkey.IsValid(snap.Key) {
		t.Errorf("vended key %q is not valid", snap.Key)
	}
	if snap.Label != "test-svc" {
		t.Errorf("expected label 'test-svc', got %q", snap.Label)
	}

	// Use the vended key in a chat completion request.
	req, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", snap.Key)

	chatResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("chat completion: %v", err)
	}
	defer chatResp.Body.Close()
	if chatResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(chatResp.Body)
		t.Fatalf("expected 200, got %d: %s", chatResp.StatusCode, body)
	}

	// Allow async goroutine to complete.
	time.Sleep(20 * time.Millisecond)

	// Check usage endpoint.
	usageResp, err := http.Get(butter.URL + "/v1/app-keys/" + snap.Key + "/usage")
	if err != nil {
		t.Fatalf("GET usage: %v", err)
	}
	defer usageResp.Body.Close()
	if usageResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(usageResp.Body)
		t.Fatalf("expected 200, got %d: %s", usageResp.StatusCode, body)
	}

	var usage appkey.UsageSnapshot
	if err := json.NewDecoder(usageResp.Body).Decode(&usage); err != nil {
		t.Fatalf("decoding usage: %v", err)
	}
	if usage.TotalRequests != 1 {
		t.Errorf("expected 1 total request, got %d", usage.TotalRequests)
	}
	if usage.NonStreamRequests != 1 {
		t.Errorf("expected 1 non-stream request, got %d", usage.NonStreamRequests)
	}
}

func TestAppKey_ListAndAggregate(t *testing.T) {
	mock := mockOpenAI(t, nil)
	cfg := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false)
	butter := cfg.build(t)

	// Vend two keys.
	for _, label := range []string{"svc-a", "svc-b"} {
		resp, err := http.Post(butter.URL+"/v1/app-keys", "application/json",
			strings.NewReader(`{"label":"`+label+`"}`))
		if err != nil {
			t.Fatalf("vend %s: %v", label, err)
		}
		resp.Body.Close()
	}

	// List endpoint.
	listResp, err := http.Get(butter.URL + "/v1/app-keys")
	if err != nil {
		t.Fatalf("GET /v1/app-keys: %v", err)
	}
	defer listResp.Body.Close()
	var list []appkey.UsageSnapshot
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatalf("decoding list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 keys, got %d", len(list))
	}

	// Aggregate endpoint.
	aggResp, err := http.Get(butter.URL + "/v1/usage")
	if err != nil {
		t.Fatalf("GET /v1/usage: %v", err)
	}
	defer aggResp.Body.Close()
	var agg struct {
		Keys int `json:"keys"`
	}
	if err := json.NewDecoder(aggResp.Body).Decode(&agg); err != nil {
		t.Fatalf("decoding aggregate: %v", err)
	}
	if agg.Keys != 2 {
		t.Errorf("expected keys=2 in aggregate, got %d", agg.Keys)
	}
}

func TestAppKey_RequireKey_Rejected(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(true). // require_key = true
		build(t)

	// Request without key should be rejected.
	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 without key, got %d", resp.StatusCode)
	}
}

func TestAppKey_RequireKey_Allowed(t *testing.T) {
	mock := mockOpenAI(t, nil)
	cfg := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(true)
	butter := cfg.build(t)

	// Vend a key.
	vendResp, err := http.Post(butter.URL+"/v1/app-keys", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("vend: %v", err)
	}
	var snap appkey.UsageSnapshot
	json.NewDecoder(vendResp.Body).Decode(&snap)
	vendResp.Body.Close()

	// Request WITH key should succeed.
	req, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", snap.Key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestAppKey_InvalidKeyFormat(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false).
		build(t)

	req, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", "not-a-valid-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid key format, got %d", resp.StatusCode)
	}
}

func TestAppKey_UnknownKey_NotProvisioned(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false).
		build(t)

	// A valid-format key that was never provisioned should pass through (no tracking).
	req, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", "btr_notprovisioned000000")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 for unknown-but-valid key, got %d: %s", resp.StatusCode, body)
	}
}

func TestAppKey_Purge(t *testing.T) {
	mock := mockOpenAI(t, nil)
	cfg := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false)
	butter := cfg.build(t)

	// Vend a key and use it once.
	vendResp, err := http.Post(butter.URL+"/v1/app-keys", "application/json", strings.NewReader(`{"label":"purge-svc"}`))
	if err != nil {
		t.Fatalf("vend: %v", err)
	}
	var snap appkey.UsageSnapshot
	if err := json.NewDecoder(vendResp.Body).Decode(&snap); err != nil {
		t.Fatalf("decoding vend: %v", err)
	}
	vendResp.Body.Close()

	chatReq, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	chatReq.Header.Set("Content-Type", "application/json")
	chatReq.Header.Set("X-Butter-App-Key", snap.Key)
	chatResp, err := http.DefaultClient.Do(chatReq)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	chatResp.Body.Close()

	// No need to wait for the chat's async RecordRequest — purge succeeds
	// regardless, and any in-flight record landing after Delete becomes a
	// silent no-op via Lookup→nil.

	// Purge.
	purgeReq, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/app-keys/"+snap.Key+"/purge", nil)
	purgeResp, err := http.DefaultClient.Do(purgeReq)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	defer purgeResp.Body.Close()
	if purgeResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(purgeResp.Body)
		t.Fatalf("expected 204 from purge, got %d: %s", purgeResp.StatusCode, body)
	}

	// Usage endpoint must now 404 — record is gone, not just inactive.
	usageResp, err := http.Get(butter.URL + "/v1/app-keys/" + snap.Key + "/usage")
	if err != nil {
		t.Fatalf("GET usage: %v", err)
	}
	defer usageResp.Body.Close()
	if usageResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after purge, got %d", usageResp.StatusCode)
	}

	// Re-purging an unknown key returns 404.
	rePurge, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/app-keys/"+snap.Key+"/purge", nil)
	rePurgeResp, err := http.DefaultClient.Do(rePurge)
	if err != nil {
		t.Fatalf("re-purge: %v", err)
	}
	rePurgeResp.Body.Close()
	if rePurgeResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 re-purging unknown key, got %d", rePurgeResp.StatusCode)
	}
}

func TestAppKey_UsageNotFound(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withAppKeys(false).
		build(t)

	resp, err := http.Get(butter.URL + "/v1/app-keys/btr_notprovisioned000000/usage")
	if err != nil {
		t.Fatalf("GET usage: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}
