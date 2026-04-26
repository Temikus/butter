package appkey

import "encoding/json"

// usagePayload is a minimal struct for extracting token counts from a
// provider response body without allocating a full response object.
type usagePayload struct {
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// ExtractUsage parses promptTokens and completionTokens from a JSON response
// body. Returns zeros if the fields are absent or the body is not valid JSON.
// Intended to be called in a background goroutine — correctness over speed.
func ExtractUsage(body []byte) (promptTokens, completionTokens int64) {
	if len(body) == 0 {
		return 0, 0
	}
	var p usagePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return 0, 0
	}
	return p.Usage.PromptTokens, p.Usage.CompletionTokens
}

// anthropicUsagePayload is a minimal struct for extracting token counts from
// an Anthropic Messages API response body.
type anthropicUsagePayload struct {
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// ExtractAnthropicUsage parses input/output tokens from an Anthropic Messages
// API response body. Anthropic's input_tokens corresponds to OpenAI's
// prompt_tokens, and output_tokens to completion_tokens — call sites pass
// the returned (input, output) pair as (promptTokens, completionTokens) to
// RecordRequest. Returns zeros if absent or invalid JSON.
func ExtractAnthropicUsage(body []byte) (input, output int64) {
	if len(body) == 0 {
		return 0, 0
	}
	var p anthropicUsagePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return 0, 0
	}
	return p.Usage.InputTokens, p.Usage.OutputTokens
}
