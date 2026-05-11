package appkey

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractUsage(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	pt, ct := ExtractUsage(body)
	if pt != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", pt)
	}
	if ct != 5 {
		t.Errorf("expected completion_tokens=5, got %d", ct)
	}
}

func TestExtractUsage_NoUsageField(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","choices":[]}`)
	pt, ct := ExtractUsage(body)
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros, got %d, %d", pt, ct)
	}
}

func TestExtractUsage_InvalidJSON(t *testing.T) {
	pt, ct := ExtractUsage([]byte(`not json`))
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros for invalid JSON, got %d, %d", pt, ct)
	}
}

func TestExtractUsage_Empty(t *testing.T) {
	pt, ct := ExtractUsage(nil)
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros for nil body, got %d, %d", pt, ct)
	}
}

func TestExtractAnthropicUsage(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","usage":{"input_tokens":42,"output_tokens":17}}`)
	in, out := ExtractAnthropicUsage(body)
	if in != 42 {
		t.Errorf("expected input_tokens=42, got %d", in)
	}
	if out != 17 {
		t.Errorf("expected output_tokens=17, got %d", out)
	}
}

func TestExtractAnthropicUsage_NoUsageField(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","content":[]}`)
	in, out := ExtractAnthropicUsage(body)
	if in != 0 || out != 0 {
		t.Errorf("expected zeros, got %d, %d", in, out)
	}
}

func TestExtractAnthropicUsage_InvalidJSON(t *testing.T) {
	in, out := ExtractAnthropicUsage([]byte(`not json`))
	if in != 0 || out != 0 {
		t.Errorf("expected zeros for invalid JSON, got %d, %d", in, out)
	}
}

func TestExtractAnthropicUsage_Empty(t *testing.T) {
	in, out := ExtractAnthropicUsage(nil)
	if in != 0 || out != 0 {
		t.Errorf("expected zeros for nil body, got %d, %d", in, out)
	}
}

// realisticAnthropicStream returns an SSE transcript matching Anthropic's
// streaming Messages API event sequence: message_start carrying input_tokens,
// content block events, message_delta with cumulative output_tokens, then
// message_stop.
func realisticAnthropicStream() string {
	return strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[],"stop_reason":null,"usage":{"input_tokens":42,"output_tokens":1}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":17}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
}

func TestAnthropicStreamUsageSink_HappyPath(t *testing.T) {
	sink := &AnthropicStreamUsageSink{}
	if _, err := sink.Write([]byte(realisticAnthropicStream())); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, out := sink.Totals()
	if in != 42 {
		t.Errorf("expected input=42, got %d", in)
	}
	if out != 17 {
		t.Errorf("expected output=17, got %d", out)
	}
}

func TestAnthropicStreamUsageSink_PartialChunks(t *testing.T) {
	stream := realisticAnthropicStream()
	sink := &AnthropicStreamUsageSink{}
	// Write one byte at a time to exercise mid-line buffering.
	for i := 0; i < len(stream); i++ {
		if _, err := sink.Write([]byte{stream[i]}); err != nil {
			t.Fatalf("write at %d: %v", i, err)
		}
	}
	in, out := sink.Totals()
	if in != 42 || out != 17 {
		t.Errorf("expected (42,17), got (%d,%d)", in, out)
	}
}

func TestAnthropicStreamUsageSink_AwkwardChunkBoundaries(t *testing.T) {
	stream := realisticAnthropicStream()
	sink := &AnthropicStreamUsageSink{}
	// Write in irregular chunks straddling event boundaries.
	chunks := []string{
		stream[:7],
		stream[7:50],
		stream[50:200],
		stream[200:600],
		stream[600:],
	}
	for _, c := range chunks {
		if _, err := sink.Write([]byte(c)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	in, out := sink.Totals()
	if in != 42 || out != 17 {
		t.Errorf("expected (42,17), got (%d,%d)", in, out)
	}
}

func TestAnthropicStreamUsageSink_OnlyMessageStart(t *testing.T) {
	sink := &AnthropicStreamUsageSink{}
	body := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":99,"output_tokens":1}}}` + "\n\n"
	if _, err := sink.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, out := sink.Totals()
	if in != 99 {
		t.Errorf("expected input=99, got %d", in)
	}
	if out != 1 {
		t.Errorf("expected output=1, got %d", out)
	}
}

func TestAnthropicStreamUsageSink_DeltaIsLastWriteWins(t *testing.T) {
	// Anthropic's output_tokens on message_delta is cumulative across the
	// stream. The sink stores the most recent value, so multiple deltas
	// should result in the last delta's value, not their sum.
	sink := &AnthropicStreamUsageSink{}
	body := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":5}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":12}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":20}}`,
		``,
	}, "\n")
	if _, err := sink.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, out := sink.Totals()
	if in != 10 || out != 20 {
		t.Errorf("expected (10,20), got (%d,%d)", in, out)
	}
}

func TestAnthropicStreamUsageSink_IgnoresMalformedAndUnknown(t *testing.T) {
	sink := &AnthropicStreamUsageSink{}
	body := strings.Join([]string{
		`event: ping`,
		`data: garbage not json`,
		`data: {"type":"unknown_event","usage":{"output_tokens":999}}`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":7,"output_tokens":1}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":11}}`,
		``,
	}, "\n")
	if _, err := sink.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, out := sink.Totals()
	if in != 7 {
		t.Errorf("expected input=7, got %d", in)
	}
	if out != 11 {
		t.Errorf("expected output=11, got %d", out)
	}
}

func TestAnthropicStreamUsageSink_NoEvents(t *testing.T) {
	sink := &AnthropicStreamUsageSink{}
	if _, err := sink.Write([]byte("event: ping\n: keepalive\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	in, out := sink.Totals()
	if in != 0 || out != 0 {
		t.Errorf("expected zeros, got (%d,%d)", in, out)
	}
}

func TestAnthropicStreamUsageSink_WriteNeverErrors(t *testing.T) {
	// Composes safely with io.MultiWriter — must always return len(p), nil.
	sink := &AnthropicStreamUsageSink{}
	for _, s := range []string{
		"",
		"\n",
		"data: {bad\n",
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n",
	} {
		n, err := sink.Write([]byte(s))
		if err != nil {
			t.Errorf("Write(%q) err=%v, want nil", s, err)
		}
		if n != len(s) {
			t.Errorf("Write(%q) n=%d, want %d", s, n, len(s))
		}
	}
}

// --- OpenAIStreamUsageSink tests ---

func TestOpenAIStreamUsageSink_HappyPath(t *testing.T) {
	sink := &OpenAIStreamUsageSink{}
	chunks := []string{
		`data: {"id":"chatcmpl-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`data: {"id":"chatcmpl-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`data: {"id":"chatcmpl-s","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"!"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
	}
	for _, c := range chunks {
		sink.FeedChunk([]byte(c))
	}
	pt, ct := sink.Totals()
	if pt != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", pt)
	}
	if ct != 5 {
		t.Errorf("expected completion_tokens=5, got %d", ct)
	}
}

func TestOpenAIStreamUsageSink_NoUsageChunks(t *testing.T) {
	sink := &OpenAIStreamUsageSink{}
	chunks := []string{
		`data: {"id":"chatcmpl-s","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"id":"chatcmpl-s","choices":[{"delta":{"content":"!"}}]}`,
	}
	for _, c := range chunks {
		sink.FeedChunk([]byte(c))
	}
	pt, ct := sink.Totals()
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros, got (%d, %d)", pt, ct)
	}
}

func TestOpenAIStreamUsageSink_NonDataLines(t *testing.T) {
	sink := &OpenAIStreamUsageSink{}
	sink.FeedChunk([]byte("event: ping"))
	sink.FeedChunk([]byte(""))
	sink.FeedChunk([]byte(": keepalive"))
	sink.FeedChunk([]byte("data: [DONE]"))
	pt, ct := sink.Totals()
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros, got (%d, %d)", pt, ct)
	}
}

func TestOpenAIStreamUsageSink_MalformedJSON(t *testing.T) {
	sink := &OpenAIStreamUsageSink{}
	sink.FeedChunk([]byte("data: {bad json"))
	sink.FeedChunk([]byte("data: not even close"))
	pt, ct := sink.Totals()
	if pt != 0 || ct != 0 {
		t.Errorf("expected zeros for malformed JSON, got (%d, %d)", pt, ct)
	}
}

func TestOpenAIStreamUsageSink_LastWriteWins(t *testing.T) {
	sink := &OpenAIStreamUsageSink{}
	sink.FeedChunk([]byte(`data: {"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	sink.FeedChunk([]byte(`data: {"usage":{"prompt_tokens":10,"completion_tokens":7}}`))
	pt, ct := sink.Totals()
	if pt != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", pt)
	}
	if ct != 7 {
		t.Errorf("expected completion_tokens=7, got %d", ct)
	}
}

// --- InjectStreamOptions tests ---

func TestInjectStreamOptions_AddsWhenAbsent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	result := InjectStreamOptions(body)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	so, ok := m["stream_options"]
	if !ok {
		t.Fatal("stream_options not found in result")
	}
	var opts struct {
		IncludeUsage bool `json:"include_usage"`
	}
	if err := json.Unmarshal(so, &opts); err != nil {
		t.Fatalf("parsing stream_options: %v", err)
	}
	if !opts.IncludeUsage {
		t.Error("expected include_usage=true")
	}
}

func TestInjectStreamOptions_NoopWhenPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":false}}`)
	result := InjectStreamOptions(body)
	if string(result) != string(body) {
		t.Errorf("expected unchanged body when stream_options present")
	}
}

func TestInjectStreamOptions_EmptyBody(t *testing.T) {
	result := InjectStreamOptions(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got %q", result)
	}
	result = InjectStreamOptions([]byte{})
	if len(result) != 0 {
		t.Errorf("expected empty for empty input, got %q", result)
	}
}

func TestInjectStreamOptions_PreservesAllFields(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true,"temperature":0.7,"max_tokens":100}`)
	result := InjectStreamOptions(body)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	for _, key := range []string{"model", "messages", "stream", "temperature", "max_tokens", "stream_options"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in result", key)
		}
	}
}

func TestInjectStreamOptions_InvalidJSON(t *testing.T) {
	body := []byte(`not json at all`)
	result := InjectStreamOptions(body)
	if string(result) != string(body) {
		t.Errorf("expected unchanged body for invalid JSON")
	}
}

func BenchmarkInjectStreamOptions(b *testing.B) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Write me a story about a brave knight"}],"stream":true,"temperature":0.7,"max_tokens":1024}`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = InjectStreamOptions(body)
	}
}
