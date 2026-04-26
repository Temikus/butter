package transport_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temikus/butter/internal/appkey"
	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/provider/openrouter"
	"github.com/temikus/butter/internal/proxy"
	"github.com/temikus/butter/internal/transport"
)

// setupLifecycleServer returns an httptest server wired with an app-key store
// and the test's mock upstream. The store is returned for direct inspection.
func setupLifecycleServer(t *testing.T, mockURL string, opts ...transport.Option) (*httptest.Server, *appkey.Store) {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: ":0", ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second},
		Providers: map[string]config.ProviderConfig{
			"openrouter": {BaseURL: mockURL, Keys: []config.KeyConfig{{Key: "test-key", Weight: 1}}},
		},
		Routing: config.RoutingConfig{DefaultProvider: "openrouter"},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	registry := provider.NewRegistry()
	registry.Register(openrouter.New(mockURL, nil))
	store := appkey.NewStore()

	allOpts := append([]transport.Option{transport.WithAppKeyStore(store, "X-Butter-App-Key", false)}, opts...)
	engine := proxy.NewEngine(registry, cfg, logger, nil)
	srv := transport.NewServer(&cfg.Server, engine, logger, nil, allOpts...)
	ts := httptest.NewServer(srv.Handler())
	return ts, store
}

func mockOpenRouter() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
}

func TestAppKeyCreate_WithTTL(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, _ := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/app-keys", "application/json",
		strings.NewReader(`{"label":"test","ttl_seconds":3600}`))
	if err != nil {
		t.Fatalf("vend: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var snap appkey.UsageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	if !snap.ExpiresAt.After(time.Now().Add(50 * time.Minute)) {
		t.Errorf("expected expires_at ~1h in future, got %v", snap.ExpiresAt)
	}
	if snap.Status != "active" {
		t.Errorf("expected status=active, got %q", snap.Status)
	}
}

func TestAppKeyCreate_DefaultTTL(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, _ := setupLifecycleServer(t, mock.URL, transport.WithAppKeyDefaultTTL(2*time.Hour))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/app-keys", "application/json", strings.NewReader(`{"label":"x"}`))
	if err != nil {
		t.Fatalf("vend: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var snap appkey.UsageSnapshot
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	if snap.ExpiresAt == nil {
		t.Fatal("expected default TTL to apply")
	}
	if !snap.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Errorf("expected ~2h expiry, got %v", snap.ExpiresAt)
	}
}

func TestAppKeyRevoke_RejectsSubsequentRequests(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, store := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	const key = "btr_revokeflow0000000000"
	store.Provision(key, "test")

	// Pre-flight: chat works.
	if status := postChat(t, ts.URL, key); status != 200 {
		t.Fatalf("pre-revoke chat: expected 200, got %d", status)
	}

	// Revoke.
	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/app-keys/"+key, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Subsequent chat should be rejected.
	if status := postChat(t, ts.URL, key); status != 401 {
		t.Errorf("post-revoke chat: expected 401, got %d", status)
	}

	// Revoked key still appears in list with status=revoked.
	listResp, _ := http.Get(ts.URL + "/v1/app-keys")
	defer func() { _ = listResp.Body.Close() }()
	var snaps []*appkey.UsageSnapshot
	if err := json.NewDecoder(listResp.Body).Decode(&snaps); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 key in list, got %d", len(snaps))
	}
	if snaps[0].Status != "revoked" {
		t.Errorf("expected status=revoked, got %q", snaps[0].Status)
	}
	if snaps[0].RevokedAt == nil {
		t.Error("expected revoked_at to be set in list")
	}
}

func TestAppKeyRevoke_Unknown(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, _ := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/app-keys/btr_doesnotexist00000000", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAppKeyRotate(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, store := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	const oldKey = "btr_rotateflow0000000000"
	store.Provision(oldKey, "production")
	store.RecordRequest(oldKey, "gpt-4o", false, 5, 3)

	resp, err := http.Post(ts.URL+"/v1/app-keys/"+oldKey+"/rotate", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}

	var pair struct {
		Old *appkey.UsageSnapshot `json:"old"`
		New *appkey.UsageSnapshot `json:"new"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pair); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pair.Old.Status != "revoked" {
		t.Errorf("expected old key status=revoked, got %q", pair.Old.Status)
	}
	if pair.Old.TotalRequests != 1 {
		t.Errorf("expected old key usage history preserved, got %d", pair.Old.TotalRequests)
	}
	if pair.New.Label != "production" {
		t.Errorf("expected new key to inherit label, got %q", pair.New.Label)
	}
	if pair.New.Status != "active" {
		t.Errorf("expected new key active, got %q", pair.New.Status)
	}

	if status := postChat(t, ts.URL, oldKey); status != 401 {
		t.Errorf("rotated old key should be rejected (401), got %d", status)
	}
	if status := postChat(t, ts.URL, pair.New.Key); status != 200 {
		t.Errorf("new key should work, got %d", status)
	}
}

func TestAppKeyUpdate_Expiry(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, store := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	const key = "btr_expireflow0000000000"
	store.Provision(key, "test")

	// PATCH with ttl_seconds:3600 — verifies the endpoint accepts ttl_seconds
	// and populates expires_at on the response.
	req, _ := http.NewRequest("PATCH", ts.URL+"/v1/app-keys/"+key, strings.NewReader(`{"ttl_seconds":3600}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var snap appkey.UsageSnapshot
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	if snap.ExpiresAt == nil {
		t.Fatal("expected expires_at set")
	}

	// Force expiry via the store directly — avoids time.Sleep flakiness while
	// still exercising middleware rejection of an expired key.
	if err := store.SetExpiry(key, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetExpiry: %v", err)
	}
	if status := postChat(t, ts.URL, key); status != 401 {
		t.Errorf("expired key: expected 401, got %d", status)
	}

	// Clear expiry via PATCH ttl_seconds:0 → key becomes active again.
	clearReq, _ := http.NewRequest("PATCH", ts.URL+"/v1/app-keys/"+key, strings.NewReader(`{"ttl_seconds":0}`))
	clearReq.Header.Set("Content-Type", "application/json")
	clearResp, _ := http.DefaultClient.Do(clearReq)
	_ = clearResp.Body.Close()
	if clearResp.StatusCode != http.StatusOK {
		t.Fatalf("clear PATCH: expected 200, got %d", clearResp.StatusCode)
	}

	if status := postChat(t, ts.URL, key); status != 200 {
		t.Errorf("after clearing expiry: expected 200, got %d", status)
	}
}

func TestAppKeyUpdate_PastTimestamp(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, store := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	const key = "btr_pasttime000000000000"
	store.Provision(key, "test")

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	body := fmt.Sprintf(`{"expires_at":"%s"}`, past)
	req, _ := http.NewRequest("PATCH", ts.URL+"/v1/app-keys/"+key, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if status := postChat(t, ts.URL, key); status != 401 {
		t.Errorf("past expiry: expected 401, got %d", status)
	}
}

func TestAppKeyUpdate_MutuallyExclusive(t *testing.T) {
	mock := mockOpenRouter()
	defer mock.Close()
	ts, store := setupLifecycleServer(t, mock.URL)
	defer ts.Close()

	const key = "btr_mutexflow0000000000a"
	store.Provision(key, "test")

	body := `{"expires_at":"2030-01-01T00:00:00Z","ttl_seconds":3600}`
	req, _ := http.NewRequest("PATCH", ts.URL+"/v1/app-keys/"+key, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// postChat issues a chat completions request with the given key and returns the status.
func postChat(t *testing.T, baseURL, key string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", baseURL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("postChat: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
