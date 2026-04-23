package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/temikus/butter/internal/provider"
)

// --- Translation unit tests ---

func TestTranslateRequest_Basic(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if req.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected model claude-sonnet-4-20250514, got %s", req.Model)
	}
	if req.MaxTokens != defaultMaxTokens {
		t.Errorf("expected max_tokens %d, got %d", defaultMaxTokens, req.MaxTokens)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("expected role user, got %s", req.Messages[0].Role)
	}
	if req.System != "" {
		t.Errorf("expected no system, got %q", req.System)
	}
}

func TestTranslateRequest_SystemMessage(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{"role": "system", "content": "You are helpful"},
			{"role": "user", "content": "Hi"}
		]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	_ = json.Unmarshal(out, &req)

	if req.System != "You are helpful" {
		t.Errorf("expected system 'You are helpful', got %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message (system extracted), got %d", len(req.Messages))
	}
}

func TestTranslateRequest_MultipleSystemMessages(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{"role": "system", "content": "Be concise"},
			{"role": "system", "content": "Be helpful"},
			{"role": "user", "content": "Hi"}
		]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	_ = json.Unmarshal(out, &req)

	if req.System != "Be concise\nBe helpful" {
		t.Errorf("expected concatenated system, got %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(req.Messages))
	}
}

func TestTranslateRequest_MaxTokensExplicit(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hi"}]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	_ = json.Unmarshal(out, &req)

	if req.MaxTokens != 100 {
		t.Errorf("expected max_tokens 100, got %d", req.MaxTokens)
	}
}

func TestTranslateRequest_StopString(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"stop": "END",
		"messages": [{"role": "user", "content": "Hi"}]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	_ = json.Unmarshal(out, &req)

	if len(req.StopSequences) != 1 || req.StopSequences[0] != "END" {
		t.Errorf("expected stop_sequences [END], got %v", req.StopSequences)
	}
}

func TestTranslateRequest_StopArray(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"stop": ["END", "STOP"],
		"messages": [{"role": "user", "content": "Hi"}]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	_ = json.Unmarshal(out, &req)

	if len(req.StopSequences) != 2 {
		t.Fatalf("expected 2 stop_sequences, got %d", len(req.StopSequences))
	}
	if req.StopSequences[0] != "END" || req.StopSequences[1] != "STOP" {
		t.Errorf("expected [END, STOP], got %v", req.StopSequences)
	}
}

func TestTranslateRequest_Temperature(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"temperature": 0.7,
		"messages": [{"role": "user", "content": "Hi"}]
	}`

	out, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req anthropicRequest
	_ = json.Unmarshal(out, &req)

	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", req.Temperature)
	}
}

func TestTranslateResponse_Basic(t *testing.T) {
	input := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"model": "claude-sonnet-4-20250514",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`

	out, err := translateResponse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp openaiResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("failed to parse output: %v", err)
	}

	if resp.ID != "msg_123" {
		t.Errorf("expected id msg_123, got %s", resp.ID)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("expected object chat.completion, got %s", resp.Object)
	}
	if resp.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected model claude-sonnet-4-20250514, got %s", resp.Model)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", resp.Choices[0].Message.Role)
	}
	if *resp.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %s", *resp.Choices[0].FinishReason)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens 10, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("expected completion_tokens 5, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens 15, got %d", resp.Usage.TotalTokens)
	}
}

func TestTranslateResponse_MultipleContentBlocks(t *testing.T) {
	input := `{
		"id": "msg_456",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "text", "text": "Hello "},
			{"type": "text", "text": "world!"}
		],
		"model": "claude-sonnet-4-20250514",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 5, "output_tokens": 3}
	}`

	out, err := translateResponse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp openaiResponse
	_ = json.Unmarshal(out, &resp)

	if resp.Choices[0].Message.Content != "Hello world!" {
		t.Errorf("expected concatenated content, got %q", resp.Choices[0].Message.Content)
	}
}

func TestTranslateResponse_StopReasons(t *testing.T) {
	tests := []struct {
		stopReason string
		want       string
	}{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
	}

	for _, tt := range tests {
		input := fmt.Sprintf(`{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hi"}],
			"model": "claude-sonnet-4-20250514",
			"stop_reason": "%s",
			"usage": {"input_tokens": 1, "output_tokens": 1}
		}`, tt.stopReason)

		out, err := translateResponse([]byte(input))
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", tt.stopReason, err)
		}

		var resp openaiResponse
		_ = json.Unmarshal(out, &resp)

		if *resp.Choices[0].FinishReason != tt.want {
			t.Errorf("stop_reason %s: expected %s, got %s", tt.stopReason, tt.want, *resp.Choices[0].FinishReason)
		}
	}
}

// --- Stream translation unit tests ---

func TestTranslateStreamEvent_MessageStart(t *testing.T) {
	data := `{"type":"message_start","message":{"id":"msg_abc","model":"claude-sonnet-4-20250514","usage":{"input_tokens":25}}}`
	state := &streamState{}

	out, done, err := translateStreamEvent("message_start", data, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("expected done=false")
	}
	if state.id != "msg_abc" {
		t.Errorf("expected state.id msg_abc, got %s", state.id)
	}
	if state.model != "claude-sonnet-4-20250514" {
		t.Errorf("expected state.model claude-sonnet-4-20250514, got %s", state.model)
	}

	var chunk openaiStreamChunk
	_ = json.Unmarshal(out, &chunk)
	if chunk.Choices[0].Delta.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", chunk.Choices[0].Delta.Role)
	}
}

func TestTranslateStreamEvent_ContentBlockDelta(t *testing.T) {
	state := &streamState{id: "msg_abc", model: "claude-sonnet-4-20250514"}
	data := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

	out, done, err := translateStreamEvent("content_block_delta", data, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("expected done=false")
	}

	var chunk openaiStreamChunk
	_ = json.Unmarshal(out, &chunk)
	if chunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", chunk.Choices[0].Delta.Content)
	}
	if chunk.ID != "msg_abc" {
		t.Errorf("expected id from state, got %s", chunk.ID)
	}
}

func TestTranslateStreamEvent_MessageDelta(t *testing.T) {
	state := &streamState{id: "msg_abc", model: "claude-sonnet-4-20250514"}
	data := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`

	out, done, err := translateStreamEvent("message_delta", data, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("expected done=false")
	}

	var chunk openaiStreamChunk
	_ = json.Unmarshal(out, &chunk)
	if *chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %v", chunk.Choices[0].FinishReason)
	}
}

func TestTranslateStreamEvent_MessageStop(t *testing.T) {
	state := &streamState{}
	out, done, err := translateStreamEvent("message_stop", `{"type":"message_stop"}`, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Error("expected done=true")
	}
	if out != nil {
		t.Errorf("expected nil output, got %s", out)
	}
}

func TestTranslateStreamEvent_Ping(t *testing.T) {
	state := &streamState{}
	out, done, err := translateStreamEvent("ping", `{"type":"ping"}`, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("expected done=false")
	}
	if out != nil {
		t.Error("expected nil output for ping")
	}
}

// --- Integration tests (httptest) ---

func TestChatCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify correct endpoint and headers.
		if r.URL.Path != "/messages" {
			t.Errorf("expected /messages, got %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key test-key, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != anthropicVersion {
			t.Errorf("expected anthropic-version %s, got %s", anthropicVersion, r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		// Verify no Bearer auth.
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected no Authorization header, got %s", auth)
		}

		// Verify translated request body.
		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to parse request: %v", err)
		}
		if req.Model != "claude-sonnet-4-20250514" {
			t.Errorf("expected model claude-sonnet-4-20250514, got %s", req.Model)
		}
		if req.System != "Be helpful" {
			t.Errorf("expected system 'Be helpful', got %q", req.System)
		}
		if req.MaxTokens != 100 {
			t.Errorf("expected max_tokens 100, got %d", req.MaxTokens)
		}

		// Return Anthropic response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, `{
			"id": "msg_test",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hi there!"}],
			"model": "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	resp, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model: "claude-sonnet-4-20250514",
		RawBody: []byte(`{
			"model": "claude-sonnet-4-20250514",
			"max_tokens": 100,
			"messages": [
				{"role": "system", "content": "Be helpful"},
				{"role": "user", "content": "Hello"}
			]
		}`),
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify translated OpenAI response.
	var oai openaiResponse
	if err := json.Unmarshal(resp.RawBody, &oai); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if oai.ID != "msg_test" {
		t.Errorf("expected id msg_test, got %s", oai.ID)
	}
	if oai.Object != "chat.completion" {
		t.Errorf("expected object chat.completion, got %s", oai.Object)
	}
	if oai.Choices[0].Message.Content != "Hi there!" {
		t.Errorf("expected content 'Hi there!', got %q", oai.Choices[0].Message.Content)
	}
	if *oai.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason stop, got %s", *oai.Choices[0].FinishReason)
	}
}

func TestChatCompletionStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		// Simulate Anthropic streaming events.
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_stream","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`+"\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`+"\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`+"\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`+"\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := New(server.URL, nil)
	stream, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:  "claude-sonnet-4-20250514",
		Stream: true,
		RawBody: []byte(`{
			"model": "claude-sonnet-4-20250514",
			"stream": true,
			"messages": [{"role": "user", "content": "Hi"}]
		}`),
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Chunk 1: role chunk from message_start
	chunk, err := stream.Next()
	if err != nil {
		t.Fatalf("error reading chunk 1: %v", err)
	}
	assertChunkHasField(t, chunk, "role", "assistant")

	// Chunk 2: "Hello" content delta
	chunk, err = stream.Next()
	if err != nil {
		t.Fatalf("error reading chunk 2: %v", err)
	}
	assertChunkHasField(t, chunk, "content", "Hello")

	// Chunk 3: " world" content delta
	chunk, err = stream.Next()
	if err != nil {
		t.Fatalf("error reading chunk 3: %v", err)
	}
	assertChunkHasField(t, chunk, "content", " world")

	// Chunk 4: finish_reason from message_delta
	chunk, err = stream.Next()
	if err != nil {
		t.Fatalf("error reading chunk 4: %v", err)
	}
	assertChunkHasFinishReason(t, chunk, "stop")

	// EOF from message_stop
	_, err = stream.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got: %v", err)
	}
}

func TestChatCompletionStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Too many requests"}}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	_, err := p.ChatCompletionStream(context.Background(), &provider.ChatRequest{
		Model:   "claude-sonnet-4-20250514",
		Stream:  true,
		RawBody: []byte(`{"model":"claude-sonnet-4-20250514","stream":true,"messages":[{"role":"user","content":"Hi"}]}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}

	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 429 {
		t.Errorf("expected status 429, got %d", pe.StatusCode)
	}
	if pe.Message != "Too many requests" {
		t.Errorf("expected 'Too many requests', got %q", pe.Message)
	}
}

func TestPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("expected /messages, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Custom") != "value" {
			t.Errorf("expected X-Custom header")
		}
		_, _ = fmt.Fprint(w, `{"id":"msg_pass"}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	headers := http.Header{"X-Custom": []string{"value"}}
	resp, err := p.Passthrough(context.Background(), "POST", "/messages", nil, headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"id":"msg_pass"}` {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestSupportsOperation(t *testing.T) {
	p := New("", nil)

	tests := []struct {
		op   provider.Operation
		want bool
	}{
		{provider.OpChatCompletion, true},
		{provider.OpPassthrough, true},
		{provider.OpEmbeddings, false},
		{provider.OpModels, false},
	}

	for _, tt := range tests {
		if got := p.SupportsOperation(tt.op); got != tt.want {
			t.Errorf("SupportsOperation(%s) = %v, want %v", tt.op, got, tt.want)
		}
	}
}

func TestProviderName(t *testing.T) {
	p := New("", nil)
	if p.Name() != "anthropic" {
		t.Errorf("expected 'anthropic', got %q", p.Name())
	}
}

func TestSetAuthHeader(t *testing.T) {
	p := New("", nil)
	h := http.Header{}
	p.SetAuthHeader(h, "sk-ant-test")

	if h.Get("x-api-key") != "sk-ant-test" {
		t.Errorf("expected x-api-key sk-ant-test, got %s", h.Get("x-api-key"))
	}
	if h.Get("anthropic-version") != anthropicVersion {
		t.Errorf("expected anthropic-version %s, got %s", anthropicVersion, h.Get("anthropic-version"))
	}
}

func TestChatCompletionNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"api_error","message":"Internal error"}}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "claude-sonnet-4-20250514",
		RawBody: []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hi"}]}`),
		APIKey:  "test-key",
	})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	pe, ok := err.(*provider.ProviderError)
	if !ok {
		t.Fatalf("expected *provider.ProviderError, got %T", err)
	}
	if pe.StatusCode != 500 {
		t.Errorf("expected status 500, got %d", pe.StatusCode)
	}
	if pe.Message != "Internal error" {
		t.Errorf("expected 'Internal error', got %q", pe.Message)
	}
}

func TestBaseURLTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("expected /messages, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id":"msg_1","type":"message","role":"assistant",
			"content":[{"type":"text","text":"ok"}],
			"model":"claude-sonnet-4-20250514","stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer server.Close()

	p := New(server.URL+"/", nil)
	_, err := p.ChatCompletion(context.Background(), &provider.ChatRequest{
		Model:   "claude-sonnet-4-20250514",
		RawBody: []byte(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"Hi"}]}`),
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Test helpers ---

func assertChunkHasField(t *testing.T, chunk []byte, field, expected string) {
	t.Helper()
	// Strip "data: " prefix.
	data := chunk
	if len(data) > 6 && string(data[:6]) == "data: " {
		data = data[6:]
	}

	var c openaiStreamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("failed to parse chunk: %v (raw: %s)", err, chunk)
	}

	if len(c.Choices) == 0 {
		t.Fatalf("expected choices in chunk, got none (raw: %s)", chunk)
	}

	switch field {
	case "role":
		if c.Choices[0].Delta.Role != expected {
			t.Errorf("expected delta.role=%q, got %q", expected, c.Choices[0].Delta.Role)
		}
	case "content":
		if c.Choices[0].Delta.Content != expected {
			t.Errorf("expected delta.content=%q, got %q", expected, c.Choices[0].Delta.Content)
		}
	}
}

func assertChunkHasFinishReason(t *testing.T, chunk []byte, expected string) {
	t.Helper()
	data := chunk
	if len(data) > 6 && string(data[:6]) == "data: " {
		data = data[6:]
	}

	var c openaiStreamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("failed to parse chunk: %v (raw: %s)", err, chunk)
	}

	if c.Choices[0].FinishReason == nil {
		t.Fatalf("expected finish_reason, got nil")
	}
	if *c.Choices[0].FinishReason != expected {
		t.Errorf("expected finish_reason=%q, got %q", expected, *c.Choices[0].FinishReason)
	}
}

func TestHandleAnthropicNative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("expected /messages, got %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		// Verify headers are forwarded.
		if r.Header.Get("x-api-key") != "sk-test" {
			t.Errorf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header, got %q", r.Header.Get("anthropic-version"))
		}
		// Verify body is forwarded unchanged.
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"claude-3","messages":[]}` {
			t.Errorf("unexpected body: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_123","type":"message","content":[{"type":"text","text":"Hello"}]}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	headers := http.Header{
		"x-api-key":         []string{"sk-test"},
		"anthropic-version": []string{"2023-06-01"},
		"Content-Type":      []string{"application/json"},
	}
	resp, err := p.HandleAnthropicNative(context.Background(), []byte(`{"model":"claude-3","messages":[]}`), headers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "msg_123") {
		t.Errorf("expected response to contain msg_123, got: %s", body)
	}
}

func TestHandleAnthropicNativeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"too many requests"}}`)
	}))
	defer server.Close()

	p := New(server.URL, nil)
	_, err := p.HandleAnthropicNative(context.Background(), []byte(`{"model":"claude-3","messages":[]}`), http.Header{})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}

	var pe *provider.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ProviderError, got %T: %v", err, err)
	}
	if pe.StatusCode != 429 {
		t.Errorf("expected status 429, got %d", pe.StatusCode)
	}
}
