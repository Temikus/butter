package gemini

import (
	"encoding/json"
	"testing"
)

func TestTranslateRequest_Basic(t *testing.T) {
	input := `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"Hello"}]}`
	body, model, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "gemini-2.0-flash" {
		t.Errorf("expected model gemini-2.0-flash, got %s", model)
	}

	var gem geminiRequest
	if err := json.Unmarshal(body, &gem); err != nil {
		t.Fatalf("failed to parse translated body: %v", err)
	}
	if len(gem.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(gem.Contents))
	}
	if gem.Contents[0].Role != "user" {
		t.Errorf("expected role user, got %s", gem.Contents[0].Role)
	}
	if gem.Contents[0].Parts[0].Text != "Hello" {
		t.Errorf("expected text Hello, got %s", gem.Contents[0].Parts[0].Text)
	}
	if gem.SystemInstruction != nil {
		t.Error("expected no system instruction")
	}
}

func TestTranslateRequest_SystemMessage(t *testing.T) {
	input := `{"model":"gemini-2.0-flash","messages":[{"role":"system","content":"Be helpful"},{"role":"user","content":"Hi"}]}`
	body, _, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gem geminiRequest
	if err := json.Unmarshal(body, &gem); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if gem.SystemInstruction == nil {
		t.Fatal("expected system instruction")
	}
	if gem.SystemInstruction.Parts[0].Text != "Be helpful" {
		t.Errorf("expected 'Be helpful', got %s", gem.SystemInstruction.Parts[0].Text)
	}
	if len(gem.Contents) != 1 {
		t.Errorf("expected 1 content (user only), got %d", len(gem.Contents))
	}
}

func TestTranslateRequest_AssistantRole(t *testing.T) {
	input := `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello!"}]}`
	body, _, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gem geminiRequest
	if err := json.Unmarshal(body, &gem); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if len(gem.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(gem.Contents))
	}
	if gem.Contents[1].Role != "model" {
		t.Errorf("expected assistant mapped to 'model', got %s", gem.Contents[1].Role)
	}
}

func TestTranslateRequest_GenerationConfig(t *testing.T) {
	input := `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"Hi"}],"temperature":0.7,"max_tokens":100,"top_p":0.9,"stop":["END"]}`
	body, _, err := translateRequest([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gem geminiRequest
	if err := json.Unmarshal(body, &gem); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if gem.GenerationConfig == nil {
		t.Fatal("expected generation config")
	}
	if *gem.GenerationConfig.Temperature != 0.7 {
		t.Errorf("expected temp 0.7, got %v", *gem.GenerationConfig.Temperature)
	}
	if *gem.GenerationConfig.MaxOutputTokens != 100 {
		t.Errorf("expected max_tokens 100, got %v", *gem.GenerationConfig.MaxOutputTokens)
	}
	if *gem.GenerationConfig.TopP != 0.9 {
		t.Errorf("expected top_p 0.9, got %v", *gem.GenerationConfig.TopP)
	}
	if len(gem.GenerationConfig.StopSequences) != 1 || gem.GenerationConfig.StopSequences[0] != "END" {
		t.Errorf("expected stop sequences [END], got %v", gem.GenerationConfig.StopSequences)
	}
}

func TestTranslateResponse_Basic(t *testing.T) {
	geminiResp := `{
		"candidates": [{
			"content": {"parts": [{"text": "Hello!"}], "role": "model"},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}
	}`
	body, err := translateResponse([]byte(geminiResp), "gemini-2.0-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var oai openaiResponse
	if err := json.Unmarshal(body, &oai); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if oai.Object != "chat.completion" {
		t.Errorf("expected object chat.completion, got %s", oai.Object)
	}
	if oai.Model != "gemini-2.0-flash" {
		t.Errorf("expected model gemini-2.0-flash, got %s", oai.Model)
	}
	if len(oai.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(oai.Choices))
	}
	if oai.Choices[0].Message.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %s", oai.Choices[0].Message.Content)
	}
	if *oai.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %s", *oai.Choices[0].FinishReason)
	}
	if oai.Usage.PromptTokens != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", oai.Usage.PromptTokens)
	}
	if oai.Usage.CompletionTokens != 5 {
		t.Errorf("expected 5 completion tokens, got %d", oai.Usage.CompletionTokens)
	}
}

func TestTranslateResponse_SafetyBlock(t *testing.T) {
	geminiResp := `{
		"candidates": [{
			"content": {"parts": [], "role": "model"},
			"finishReason": "SAFETY"
		}]
	}`
	body, err := translateResponse([]byte(geminiResp), "gemini-2.0-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var oai openaiResponse
	if err := json.Unmarshal(body, &oai); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if *oai.Choices[0].FinishReason != "content_filter" {
		t.Errorf("expected finish_reason 'content_filter', got %s", *oai.Choices[0].FinishReason)
	}
}

func TestMapFinishReason(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"RECITATION", "content_filter"},
	}
	for _, tt := range tests {
		got := mapFinishReason(tt.input)
		if got == nil || *got != tt.want {
			t.Errorf("mapFinishReason(%s) = %v, want %s", tt.input, got, tt.want)
		}
	}

	if got := mapFinishReason(""); got != nil {
		t.Errorf("mapFinishReason('') = %v, want nil", *got)
	}
}

func TestTranslateStreamChunk(t *testing.T) {
	state := &streamState{model: "gemini-2.0-flash", id: "test-id"}
	data := `{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}]}`

	out, done, err := translateStreamChunk([]byte(data), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("expected done=false for chunk without finishReason")
	}

	var chunk openaiStreamChunk
	if err := json.Unmarshal(out, &chunk); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if chunk.Object != "chat.completion.chunk" {
		t.Errorf("expected object chat.completion.chunk, got %s", chunk.Object)
	}
	if chunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %s", chunk.Choices[0].Delta.Content)
	}
}

func TestTranslateStreamChunk_WithFinish(t *testing.T) {
	state := &streamState{model: "gemini-2.0-flash", id: "test-id"}
	data := `{"candidates":[{"content":{"parts":[{"text":"!"}],"role":"model"},"finishReason":"STOP"}]}`

	_, done, err := translateStreamChunk([]byte(data), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Error("expected done=true for chunk with finishReason")
	}
}

func TestTranslateStreamChunk_NoCandidates(t *testing.T) {
	state := &streamState{model: "gemini-2.0-flash", id: "test-id"}
	data := `{"candidates":[]}`

	out, done, err := translateStreamChunk([]byte(data), state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Error("expected done=false")
	}
	if out != nil {
		t.Errorf("expected nil output for empty candidates, got %s", out)
	}
}
