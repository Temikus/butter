package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// Operation represents a supported API operation.
type Operation string

const (
	OpChatCompletion Operation = "chat_completion"
	OpEmbeddings     Operation = "embeddings"
	OpPassthrough    Operation = "passthrough"
	OpModels         Operation = "models"
)

// Provider is the interface that all AI providers must implement.
type Provider interface {
	// Name returns the provider identifier (e.g., "openai", "openrouter").
	Name() string

	// ChatCompletion sends a non-streaming chat completion request.
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)

	// ChatCompletionStream sends a streaming chat completion request.
	ChatCompletionStream(ctx context.Context, req *ChatRequest) (Stream, error)

	// Passthrough forwards a raw HTTP request to the provider unchanged.
	Passthrough(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error)

	// SupportsOperation checks if the provider supports a given operation.
	SupportsOperation(op Operation) bool
}

// Stream represents a server-sent events stream from a provider.
type Stream interface {
	// Next returns the next SSE data line. Returns io.EOF when done.
	Next() ([]byte, error)
	// Close releases the underlying connection.
	Close() error
}

// ChatRequest is the unified chat completion request (OpenAI-compatible).
type ChatRequest struct {
	Model       string         `json:"model"`
	Messages    []Message      `json:"messages"`
	Stream      bool           `json:"stream,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	TopP        *float64       `json:"top_p,omitempty"`
	Stop        any            `json:"stop,omitempty"`
	// RawBody preserves the original request body for passthrough/unknown fields.
	RawBody     []byte         `json:"-"`
	// APIKey is set by the proxy engine before dispatch.
	APIKey      string         `json:"-"`
}

type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []ContentPart
}

// ProviderError wraps an upstream HTTP error so the engine can inspect
// the status code for retry/failover decisions.
type ProviderError struct {
	StatusCode int
	Message    string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error (status %d): %s", e.StatusCode, e.Message)
}

// ChatResponse is the unified non-streaming response.
type ChatResponse struct {
	// RawBody is the raw JSON response from the provider.
	RawBody    []byte
	StatusCode int
	Headers    http.Header
}
