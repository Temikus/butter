package plugin

import (
	"context"
	"net/http"
	"time"
)

// Plugin is the base interface all plugins must implement.
type Plugin interface {
	Name() string
	Init(cfg map[string]any) error
	Close() error
}

// TransportPlugin hooks into the HTTP transport layer.
type TransportPlugin interface {
	Plugin
	PreHTTP(ctx *RequestContext) error
	PostHTTP(ctx *RequestContext) error
	StreamChunk(ctx *RequestContext, chunk []byte) ([]byte, error)
}

// LLMPlugin hooks into the provider call layer.
type LLMPlugin interface {
	Plugin
	PreLLM(ctx *RequestContext) (*RequestContext, error)
	PostLLM(ctx *RequestContext, resp *Response) (*Response, error)
}

// ObservabilityPlugin receives completed request traces asynchronously.
type ObservabilityPlugin interface {
	Plugin
	OnTrace(trace *RequestTrace)
}

// RequestContext carries request data through the plugin chain.
type RequestContext struct {
	Request   *http.Request
	Provider  string
	Model     string
	Body      []byte
	Metadata  map[string]any
	StartTime time.Time
}

// Response wraps the provider response for PostLLM hooks.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// RequestTrace holds timing and outcome data for observability plugins.
type RequestTrace struct {
	Provider   string
	Model      string
	StatusCode int
	Duration   time.Duration
	Error      error
	Metadata   map[string]any
}

type ctxKey struct{}

// WithRequestContext stores a RequestContext in the context.
func WithRequestContext(ctx context.Context, rc *RequestContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, rc)
}

// GetRequestContext retrieves the RequestContext from the context.
func GetRequestContext(ctx context.Context) *RequestContext {
	rc, _ := ctx.Value(ctxKey{}).(*RequestContext)
	return rc
}
