package plugin

import (
	"log/slog"
)

// Chain executes plugin hooks in the correct order.
// Pre-hooks run in registration order; post-hooks run in reverse.
// All errors are logged and swallowed (fail-open).
type Chain struct {
	manager *Manager
	logger  *slog.Logger
}

// NewChain creates a new plugin chain executor.
func NewChain(m *Manager, logger *slog.Logger) *Chain {
	return &Chain{manager: m, logger: logger}
}

// RunPreHTTP executes TransportPlugin.PreHTTP in registration order.
func (c *Chain) RunPreHTTP(ctx *RequestContext) {
	for _, p := range c.manager.transport {
		if err := p.PreHTTP(ctx); err != nil {
			c.logger.Error("plugin PreHTTP error", "plugin", p.Name(), "error", err)
		}
	}
}

// RunPostHTTP executes TransportPlugin.PostHTTP in reverse registration order.
func (c *Chain) RunPostHTTP(ctx *RequestContext) {
	plugins := c.manager.transport
	for i := len(plugins) - 1; i >= 0; i-- {
		if err := plugins[i].PostHTTP(ctx); err != nil {
			c.logger.Error("plugin PostHTTP error", "plugin", plugins[i].Name(), "error", err)
		}
	}
}

// RunStreamChunk executes TransportPlugin.StreamChunk in reverse registration order.
// Returns the (possibly modified) chunk. On error, the previous chunk value is preserved.
func (c *Chain) RunStreamChunk(ctx *RequestContext, chunk []byte) []byte {
	plugins := c.manager.transport
	for i := len(plugins) - 1; i >= 0; i-- {
		modified, err := plugins[i].StreamChunk(ctx, chunk)
		if err != nil {
			c.logger.Error("plugin StreamChunk error", "plugin", plugins[i].Name(), "error", err)
			continue
		}
		chunk = modified
	}
	return chunk
}

// RunPreLLM executes LLMPlugin.PreLLM in registration order.
// Returns the (possibly modified) RequestContext.
func (c *Chain) RunPreLLM(ctx *RequestContext) *RequestContext {
	for _, p := range c.manager.llm {
		modified, err := p.PreLLM(ctx)
		if err != nil {
			c.logger.Error("plugin PreLLM error", "plugin", p.Name(), "error", err)
			continue
		}
		ctx = modified
	}
	return ctx
}

// RunPostLLM executes LLMPlugin.PostLLM in reverse registration order.
// Returns the (possibly modified) Response.
func (c *Chain) RunPostLLM(ctx *RequestContext, resp *Response) *Response {
	plugins := c.manager.llm
	for i := len(plugins) - 1; i >= 0; i-- {
		modified, err := plugins[i].PostLLM(ctx, resp)
		if err != nil {
			c.logger.Error("plugin PostLLM error", "plugin", plugins[i].Name(), "error", err)
			continue
		}
		resp = modified
	}
	return resp
}

// EmitTrace sends a RequestTrace to all ObservabilityPlugins asynchronously.
func (c *Chain) EmitTrace(trace *RequestTrace) {
	for _, p := range c.manager.observability {
		go func(p ObservabilityPlugin) {
			defer func() {
				if r := recover(); r != nil {
					c.logger.Error("plugin OnTrace panic", "plugin", p.Name(), "panic", r)
				}
			}()
			p.OnTrace(trace)
		}(p)
	}
}
