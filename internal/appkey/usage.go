package appkey

import (
	"bytes"
	"encoding/json"
)

// InjectStreamOptions adds "stream_options":{"include_usage":true} to the raw
// JSON request body so that OpenAI-compatible providers include token usage in
// the final streaming chunk. Returns the original body unchanged if
// stream_options is already present or the body is not valid JSON.
func InjectStreamOptions(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if bytes.Contains(body, []byte(`"stream_options"`)) {
		return body
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	m["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

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

// AnthropicStreamUsageSink is an io.Writer that observes a relay of Anthropic
// Messages API SSE bytes and accumulates the most recent input/output token
// counts seen on message_start and message_delta events. It tolerates Write
// chunks that split events mid-line by buffering until a newline arrives.
//
// Write never returns an error: malformed JSON or non-data lines are ignored
// so that an upstream parsing hiccup cannot break a streaming relay when
// composed via io.MultiWriter.
//
// Not safe for concurrent use — intended to be written by a single io.Copy
// goroutine and read once via Totals() after the copy returns.
type AnthropicStreamUsageSink struct {
	buf    bytes.Buffer
	input  int64
	output int64
}

// anthropicStreamEvent matches the fields the sink cares about. Anthropic
// places usage under message.usage on message_start, and at the top level
// on message_delta. Other event types are ignored.
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

const anthropicSSEDataPrefix = "data: "

// Write absorbs a chunk of SSE bytes, splits on newlines, and updates totals
// from any complete `data: {...}` event payloads found.
func (s *AnthropicStreamUsageSink) Write(p []byte) (int, error) {
	s.buf.Write(p)
	for {
		raw := s.buf.Bytes()
		idx := bytes.IndexByte(raw, '\n')
		if idx < 0 {
			break
		}
		line := s.buf.Next(idx + 1)
		line = bytes.TrimRight(line, "\r\n")
		if !bytes.HasPrefix(line, []byte(anthropicSSEDataPrefix)) {
			continue
		}
		payload := line[len(anthropicSSEDataPrefix):]
		var ev anthropicStreamEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Usage.InputTokens > 0 {
				s.input = ev.Message.Usage.InputTokens
			}
			if ev.Message.Usage.OutputTokens > 0 {
				s.output = ev.Message.Usage.OutputTokens
			}
		case "message_delta":
			if ev.Usage.InputTokens > 0 {
				s.input = ev.Usage.InputTokens
			}
			if ev.Usage.OutputTokens > 0 {
				s.output = ev.Usage.OutputTokens
			}
		}
	}
	return len(p), nil
}

// Totals returns the most recently observed input and output token counts.
// Output tokens on Anthropic message_delta events are cumulative, so the
// final value seen represents the full response usage.
func (s *AnthropicStreamUsageSink) Totals() (input, output int64) {
	return s.input, s.output
}

// OpenAIStreamUsageSink accumulates token counts from OpenAI-format SSE
// streaming chunks. Fed individual SSE data lines via FeedChunk in a
// pull-model loop (the /v1/chat/completions path uses stream.Next(), not
// io.Copy). OpenAI includes usage data in the final chunk before [DONE]
// when stream_options.include_usage is true.
//
// Not safe for concurrent use — intended to be called from a single
// stream.Next() loop and read once via Totals() after the loop returns.
type OpenAIStreamUsageSink struct {
	prompt     int64
	completion int64
}

type openaiChunkUsage struct {
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

const openaiSSEDataPrefix = "data: "

// FeedChunk parses a single SSE data line (including "data: " prefix) and
// updates token counts if a usage field is present.
func (s *OpenAIStreamUsageSink) FeedChunk(chunk []byte) {
	if !bytes.HasPrefix(chunk, []byte(openaiSSEDataPrefix)) {
		return
	}
	payload := chunk[len(openaiSSEDataPrefix):]
	if len(payload) == 0 || payload[0] != '{' {
		return
	}
	var cu openaiChunkUsage
	if err := json.Unmarshal(payload, &cu); err != nil {
		return
	}
	if cu.Usage == nil {
		return
	}
	if cu.Usage.PromptTokens > 0 {
		s.prompt = cu.Usage.PromptTokens
	}
	if cu.Usage.CompletionTokens > 0 {
		s.completion = cu.Usage.CompletionTokens
	}
}

// Totals returns the most recently observed prompt and completion token counts.
func (s *OpenAIStreamUsageSink) Totals() (prompt, completion int64) {
	return s.prompt, s.completion
}
