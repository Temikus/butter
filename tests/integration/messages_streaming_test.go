//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/temikus/butter/internal/appkey"
)

// TestMessages_StreamingUsage verifies that streaming /v1/messages requests
// record per-model token counts extracted from Anthropic message_start
// (input_tokens) and message_delta (cumulative output_tokens) events,
// instead of the previous (0, 0) zero-record.
func TestMessages_StreamingUsage(t *testing.T) {
	mock := mockAnthropic(t, anthropicStream)
	cfg := newServerCfg().
		withProvider("anthropic", mock.URL).
		withDefault("anthropic").
		withAppKeys(false)
	butter := cfg.build(t)

	// Vend a key.
	vendResp, err := http.Post(butter.URL+"/v1/app-keys", "application/json", strings.NewReader(`{"label":"stream-svc"}`))
	if err != nil {
		t.Fatalf("vend: %v", err)
	}
	var snap appkey.UsageSnapshot
	if err := json.NewDecoder(vendResp.Body).Decode(&snap); err != nil {
		t.Fatalf("decoding vend response: %v", err)
	}
	vendResp.Body.Close()

	// Streaming request to /v1/messages.
	body := `{"model":"claude-3-5-sonnet-20241022","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, butter.URL+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Butter-App-Key", snap.Key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("streaming /v1/messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, raw)
	}
	// Drain the stream so the relay (and the tee'd sink) process every byte
	// before we check usage.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("draining stream: %v", err)
	}

	// Wait for the async RecordRequest to land per-model token counts.
	// The mockAnthropic stream emits input_tokens=10 in message_start and
	// output_tokens=5 in the final message_delta.
	usage := waitForUsage(t, butter.URL, snap.Key, func(u *appkey.UsageSnapshot) bool {
		m := u.Models["claude-3-5-sonnet-20241022"]
		return m != nil && m.PromptTokens == 10 && m.CompletionTokens == 5
	})

	if usage.StreamRequests != 1 {
		t.Errorf("expected 1 stream request, got %d", usage.StreamRequests)
	}
	model := usage.Models["claude-3-5-sonnet-20241022"]
	if model.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens=10 from message_start input_tokens, got %d", model.PromptTokens)
	}
	if model.CompletionTokens != 5 {
		t.Errorf("expected completion_tokens=5 from message_delta output_tokens, got %d", model.CompletionTokens)
	}
}
