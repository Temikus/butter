//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		build(t)

	resp, err := http.Get(butter.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("expected body 'ok', got %q", body)
	}
}

func TestPassthrough_NativeEndpoint(t *testing.T) {
	mock := mockOpenAI(t, nil) // catch-all handler returns {"path": "..."}
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		build(t)

	// GET /native/openai/v1/models → proxied to mock as GET /v1/models
	resp, err := http.Get(butter.URL + "/native/openai/v1/models")
	if err != nil {
		t.Fatalf("passthrough request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// The mock catch-all returns the requested path; verify it was proxied correctly.
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding passthrough response: %v", err)
	}
	if result["path"] != "/v1/models" {
		t.Errorf("expected proxied path '/v1/models', got: %v", result["path"])
	}
}

func TestPassthrough_UnknownProviderReturns502(t *testing.T) {
	butter := newServerCfg().build(t) // no providers registered

	resp, err := http.Get(butter.URL + "/native/nonexistent/v1/models")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502 for unknown provider, got %d", resp.StatusCode)
	}
}

// TestPassthrough_OverCapFailsDispatch verifies the native passthrough relay is
// bounded: an over-cap body trips MaxBytesReader while copying to the upstream,
// failing the dispatch (502) rather than forwarding unbounded.
//
// Note on timing: this test takes ~500ms even though the request itself returns
// in ~2ms. Because the handler returns without reading the full inbound body
// (MaxBytesReader caps it), net/http performs a "lingering close" on the
// connection — it holds it open for rstAvoidanceDelay (a hard-coded 500ms in the
// stdlib) so the client can read the 502 before a TCP RST. httptest.Server.Close()
// waits on that. The delay is a fixed stdlib constant, not load-dependent, so the
// test is deterministic (not flaky on slow CI).
func TestPassthrough_OverCapFailsDispatch(t *testing.T) {
	mock := mockOpenAI(t, nil)
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withMaxRequestBytes(1024). // 1 KiB cap
		build(t)

	oversized := strings.Repeat("a", 4096)
	resp, err := http.Post(butter.URL+"/native/openai/v1/chat/completions",
		"application/json", strings.NewReader(oversized))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 502 for over-cap passthrough, got %d: %s", resp.StatusCode, body)
	}
}

func TestContentType_JSONOnError(t *testing.T) {
	mock := mockOpenAI(t, errorHandler(http.StatusInternalServerError))
	butter := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		build(t)

	resp, err := http.Post(butter.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqGPT4o))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected application/json Content-Type on error, got: %s", ct)
	}
}

func TestMethodNotAllowed_GETOnCompletions(t *testing.T) {
	butter := newServerCfg().build(t)

	// GET is not registered for /v1/chat/completions; expect 405.
	resp, err := http.Get(butter.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for GET /v1/chat/completions, got %d", resp.StatusCode)
	}
}
