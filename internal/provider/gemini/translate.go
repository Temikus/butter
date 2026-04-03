package gemini

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// --- Request translation (OpenAI → Gemini) ---

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Stream      bool            `json:"stream,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        json.RawMessage `json:"stop,omitempty"`
}

type openaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or array
}

type geminiRequest struct {
	Contents         []geminiContent    `json:"contents"`
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
	GenerationConfig *generationConfig  `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type generationConfig struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int    `json:"maxOutputTokens,omitempty"`
	TopP           *float64 `json:"topP,omitempty"`
	StopSequences  []string `json:"stopSequences,omitempty"`
}

// translateRequest converts an OpenAI-format request body to Gemini generateContent format.
// Returns the translated body and the model name (needed for URL path construction).
func translateRequest(rawBody []byte) ([]byte, string, error) {
	var oai openaiRequest
	if err := json.Unmarshal(rawBody, &oai); err != nil {
		return nil, "", fmt.Errorf("parsing request: %w", err)
	}

	gem := geminiRequest{}

	// Build generation config.
	gc := &generationConfig{
		Temperature:    oai.Temperature,
		MaxOutputTokens: oai.MaxTokens,
		TopP:           oai.TopP,
	}
	if len(oai.Stop) > 0 {
		seqs, err := parseStopField(oai.Stop)
		if err != nil {
			return nil, "", fmt.Errorf("parsing stop field: %w", err)
		}
		gc.StopSequences = seqs
	}
	if gc.Temperature != nil || gc.MaxOutputTokens != nil || gc.TopP != nil || gc.StopSequences != nil {
		gem.GenerationConfig = gc
	}

	// Separate system messages and conversation messages.
	var systemParts []string
	for _, msg := range oai.Messages {
		if msg.Role == "system" {
			text, err := extractTextContent(msg.Content)
			if err != nil {
				return nil, "", fmt.Errorf("parsing system message: %w", err)
			}
			systemParts = append(systemParts, text)
			continue
		}

		text, err := extractTextContent(msg.Content)
		if err != nil {
			return nil, "", fmt.Errorf("parsing message content: %w", err)
		}

		role := mapRoleToGemini(msg.Role)
		gem.Contents = append(gem.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: text}},
		})
	}

	if len(systemParts) > 0 {
		gem.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: strings.Join(systemParts, "\n")}},
		}
	}

	body, err := json.Marshal(gem)
	if err != nil {
		return nil, "", err
	}
	return body, oai.Model, nil
}

func mapRoleToGemini(role string) string {
	switch role {
	case "assistant":
		return "model"
	default:
		return role // "user" stays "user"
	}
}

// extractTextContent extracts text from a message content field that can be
// a JSON string or an array of content parts.
func extractTextContent(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

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

// --- Response translation (Gemini → OpenAI) ---

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
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
	Index        int       `json:"index"`
	Message      openaiMsg `json:"message"`
	FinishReason *string   `json:"finish_reason"`
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

func translateResponse(body []byte, model string) ([]byte, error) {
	var gem geminiResponse
	if err := json.Unmarshal(body, &gem); err != nil {
		return nil, fmt.Errorf("parsing gemini response: %w", err)
	}

	var content string
	var finishReason *string
	if len(gem.Candidates) > 0 {
		c := gem.Candidates[0]
		for _, part := range c.Content.Parts {
			content += part.Text
		}
		finishReason = mapFinishReason(c.FinishReason)
	}

	oai := openaiResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openaiChoice{
			{
				Index:        0,
				Message:      openaiMsg{Role: "assistant", Content: content},
				FinishReason: finishReason,
			},
		},
	}

	if gem.UsageMetadata != nil {
		oai.Usage = openaiUsage{
			PromptTokens:     gem.UsageMetadata.PromptTokenCount,
			CompletionTokens: gem.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gem.UsageMetadata.TotalTokenCount,
		}
	}

	return json.Marshal(oai)
}

func mapFinishReason(reason string) *string {
	var mapped string
	switch reason {
	case "STOP":
		mapped = "stop"
	case "MAX_TOKENS":
		mapped = "length"
	case "SAFETY":
		mapped = "content_filter"
	case "RECITATION":
		mapped = "content_filter"
	default:
		if reason == "" {
			return nil
		}
		mapped = reason
	}
	return &mapped
}

// --- Streaming translation (Gemini SSE → OpenAI SSE) ---

type streamState struct {
	model string
	id    string
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

// translateStreamChunk translates a Gemini streaming data payload into an
// OpenAI-format SSE chunk. Returns (jsonBytes, done, error).
func translateStreamChunk(data []byte, state *streamState) ([]byte, bool, error) {
	var gem geminiResponse
	if err := json.Unmarshal(data, &gem); err != nil {
		return nil, false, fmt.Errorf("parsing gemini stream chunk: %w", err)
	}

	if len(gem.Candidates) == 0 {
		return nil, false, nil // skip chunks with no candidates (e.g. safety only)
	}

	candidate := gem.Candidates[0]

	// Build content from parts.
	var content string
	for _, part := range candidate.Content.Parts {
		content += part.Text
	}

	finishReason := mapFinishReason(candidate.FinishReason)

	chunk := openaiStreamChunk{
		ID:      state.id,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   state.model,
		Choices: []openaiStreamChoice{
			{
				Index:        0,
				Delta:        openaiStreamDelta{Content: content},
				FinishReason: finishReason,
			},
		},
	}

	out, err := json.Marshal(chunk)
	if err != nil {
		return nil, false, err
	}

	done := candidate.FinishReason != ""
	return out, done, nil
}
