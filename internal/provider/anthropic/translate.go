package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultMaxTokens is used when the incoming OpenAI request omits max_tokens,
// since Anthropic requires it.
const defaultMaxTokens = 4096

// --- Request translation (OpenAI → Anthropic) ---

// openaiRequest is the subset of OpenAI chat completion fields we translate.
type openaiRequest struct {
	Model       string            `json:"model"`
	Messages    []openaiMessage   `json:"messages"`
	Stream      bool              `json:"stream,omitempty"`
	MaxTokens   *int              `json:"max_tokens,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
	TopP        *float64          `json:"top_p,omitempty"`
	Stop        json.RawMessage   `json:"stop,omitempty"`
}

type openaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or array
}

type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // pass through as-is
}

// translateRequest converts an OpenAI-format request body to Anthropic Messages API format.
func translateRequest(rawBody []byte) ([]byte, error) {
	var oai openaiRequest
	if err := json.Unmarshal(rawBody, &oai); err != nil {
		return nil, fmt.Errorf("parsing request: %w", err)
	}

	ant := anthropicRequest{
		Model:       oai.Model,
		Stream:      oai.Stream,
		Temperature: oai.Temperature,
		TopP:        oai.TopP,
	}

	// max_tokens: required by Anthropic
	if oai.MaxTokens != nil {
		ant.MaxTokens = *oai.MaxTokens
	} else {
		ant.MaxTokens = defaultMaxTokens
	}

	// Extract system messages and separate from conversation messages.
	var systemParts []string
	for _, msg := range oai.Messages {
		if msg.Role == "system" {
			text, err := extractTextContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("parsing system message: %w", err)
			}
			systemParts = append(systemParts, text)
		} else {
			ant.Messages = append(ant.Messages, anthropicMessage(msg))
		}
	}
	if len(systemParts) > 0 {
		ant.System = strings.Join(systemParts, "\n")
	}

	// Map "stop" (string or []string) to "stop_sequences" ([]string).
	if len(oai.Stop) > 0 {
		seqs, err := parseStopField(oai.Stop)
		if err != nil {
			return nil, fmt.Errorf("parsing stop field: %w", err)
		}
		ant.StopSequences = seqs
	}

	return json.Marshal(ant)
}

// extractTextContent extracts text from a message content field that can be
// a JSON string or an array of content parts.
func extractTextContent(raw json.RawMessage) (string, error) {
	// Try string first (most common).
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

	// Try array of content parts.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("content is neither string nor array: %s", raw)
	}

	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, ""), nil
}

// parseStopField handles the OpenAI "stop" field which can be a string or []string.
func parseStopField(raw json.RawMessage) ([]string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}, nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("stop field is neither string nor array")
	}
	return arr, nil
}

// --- Response translation (Anthropic → OpenAI) ---

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type openaiResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   openaiUsage    `json:"usage"`
}

type openaiChoice struct {
	Index        int           `json:"index"`
	Message      openaiMsg     `json:"message"`
	FinishReason *string       `json:"finish_reason"`
}

type openaiMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// translateResponse converts an Anthropic Messages API response to OpenAI format.
func translateResponse(body []byte) ([]byte, error) {
	var ant anthropicResponse
	if err := json.Unmarshal(body, &ant); err != nil {
		return nil, fmt.Errorf("parsing anthropic response: %w", err)
	}

	// Concatenate text content blocks.
	var content strings.Builder
	for _, block := range ant.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
	}

	finishReason := mapStopReason(ant.StopReason)

	oai := openaiResponse{
		ID:      ant.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ant.Model,
		Choices: []openaiChoice{
			{
				Index:        0,
				Message:      openaiMsg{Role: "assistant", Content: content.String()},
				FinishReason: finishReason,
			},
		},
		Usage: openaiUsage{
			PromptTokens:     ant.Usage.InputTokens,
			CompletionTokens: ant.Usage.OutputTokens,
			TotalTokens:      ant.Usage.InputTokens + ant.Usage.OutputTokens,
		},
	}

	return json.Marshal(oai)
}

// mapStopReason maps Anthropic stop_reason to OpenAI finish_reason.
func mapStopReason(reason string) *string {
	var mapped string
	switch reason {
	case "end_turn":
		mapped = "stop"
	case "max_tokens":
		mapped = "length"
	case "stop_sequence":
		mapped = "stop"
	default:
		if reason == "" {
			return nil
		}
		mapped = reason
	}
	return &mapped
}

// --- Streaming translation (Anthropic SSE events → OpenAI SSE chunks) ---

// streamState tracks state across streaming events.
type streamState struct {
	id    string
	model string
}

type openaiStreamChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []openaiStreamChoice `json:"choices"`
}

type openaiStreamChoice struct {
	Index        int                `json:"index"`
	Delta        openaiStreamDelta  `json:"delta"`
	FinishReason *string            `json:"finish_reason"`
}

type openaiStreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// translateStreamEvent translates a single Anthropic SSE event into an OpenAI-format
// SSE data line. Returns (data, done, error). If data is nil and done is false,
// the event should be skipped.
func translateStreamEvent(eventType, data string, state *streamState) ([]byte, bool, error) {
	switch eventType {
	case "message_start":
		return handleMessageStart(data, state)
	case "content_block_delta":
		return handleContentBlockDelta(data, state)
	case "message_delta":
		return handleMessageDelta(data, state)
	case "message_stop":
		return nil, true, nil
	case "ping", "content_block_start", "content_block_stop":
		return nil, false, nil // skip
	default:
		return nil, false, nil // skip unknown events
	}
}

func handleMessageStart(data string, state *streamState) ([]byte, bool, error) {
	var ev struct {
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil, false, fmt.Errorf("parsing message_start: %w", err)
	}

	state.id = ev.Message.ID
	state.model = ev.Message.Model

	chunk := openaiStreamChunk{
		ID:      state.id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   state.model,
		Choices: []openaiStreamChoice{
			{
				Index: 0,
				Delta: openaiStreamDelta{Role: "assistant"},
			},
		},
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func handleContentBlockDelta(data string, state *streamState) ([]byte, bool, error) {
	var ev struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil, false, fmt.Errorf("parsing content_block_delta: %w", err)
	}

	if ev.Delta.Type != "text_delta" {
		return nil, false, nil // skip non-text deltas
	}

	chunk := openaiStreamChunk{
		ID:      state.id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   state.model,
		Choices: []openaiStreamChoice{
			{
				Index: 0,
				Delta: openaiStreamDelta{Content: ev.Delta.Text},
			},
		},
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}

func handleMessageDelta(data string, state *streamState) ([]byte, bool, error) {
	var ev struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return nil, false, fmt.Errorf("parsing message_delta: %w", err)
	}

	finishReason := mapStopReason(ev.Delta.StopReason)

	chunk := openaiStreamChunk{
		ID:      state.id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   state.model,
		Choices: []openaiStreamChoice{
			{
				Index:        0,
				Delta:        openaiStreamDelta{},
				FinishReason: finishReason,
			},
		},
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return nil, false, err
	}
	return out, false, nil
}
