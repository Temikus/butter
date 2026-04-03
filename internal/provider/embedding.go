package provider

import (
	"context"
	"net/http"
)

// EmbeddingRequest is the unified embedding request (OpenAI-compatible).
type EmbeddingRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string or []string
	// RawBody preserves the original request body for passthrough/unknown fields.
	RawBody []byte `json:"-"`
	// APIKey is set by the proxy engine before dispatch.
	APIKey string `json:"-"`
}

// EmbeddingResponse is the unified embedding response.
type EmbeddingResponse struct {
	// RawBody is the raw JSON response from the provider.
	RawBody    []byte
	StatusCode int
	Headers    http.Header
}

// EmbeddingProvider is an optional interface for providers that support embeddings.
// Providers that do not support embeddings simply don't implement this interface.
type EmbeddingProvider interface {
	Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)
}
