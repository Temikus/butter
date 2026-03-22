package requestlog

import (
	"context"
	"log/slog"
	"strings"

	"github.com/temikus/butter/internal/plugin"
)

// Plugin logs completed request traces via slog.
// Implements plugin.ObservabilityPlugin.
type Plugin struct {
	logger       *slog.Logger
	level        slog.Level
	logBodies    bool
	bodyMaxBytes int
}

// New creates a request logging plugin that uses the given logger.
func New(logger *slog.Logger) *Plugin {
	return &Plugin{
		logger:       logger,
		level:        slog.LevelInfo,
		bodyMaxBytes: 1024,
	}
}

func (p *Plugin) Name() string { return "requestlog" }

func (p *Plugin) Init(cfg map[string]any) error {
	if cfg == nil {
		return nil
	}
	if v, ok := cfg["level"].(string); ok {
		switch strings.ToLower(v) {
		case "debug":
			p.level = slog.LevelDebug
		case "warn", "warning":
			p.level = slog.LevelWarn
		case "error":
			p.level = slog.LevelError
		default:
			p.level = slog.LevelInfo
		}
	}
	if v, ok := cfg["log_bodies"].(bool); ok {
		p.logBodies = v
	}
	if v, ok := cfg["body_max_bytes"].(int); ok && v > 0 {
		p.bodyMaxBytes = v
	}
	return nil
}

func (p *Plugin) Close() error { return nil }

// OnTrace logs a structured line for each completed request.
func (p *Plugin) OnTrace(trace *plugin.RequestTrace) {
	attrs := []any{
		"provider", trace.Provider,
		"model", trace.Model,
		"status", trace.StatusCode,
		"duration_ms", trace.Duration.Milliseconds(),
	}

	if trace.Metadata != nil {
		if v, ok := trace.Metadata["method"].(string); ok {
			attrs = append(attrs, "method", v)
		}
		if v, ok := trace.Metadata["path"].(string); ok {
			attrs = append(attrs, "path", v)
		}
		if v, ok := trace.Metadata["streaming"].(bool); ok {
			attrs = append(attrs, "streaming", v)
		}
		if p.logBodies {
			if v, ok := trace.Metadata["request_body"].([]byte); ok {
				attrs = append(attrs, "request_body", truncate(v, p.bodyMaxBytes))
			}
			if v, ok := trace.Metadata["response_body"].([]byte); ok {
				attrs = append(attrs, "response_body", truncate(v, p.bodyMaxBytes))
			}
		}
	}

	if trace.Error != nil {
		attrs = append(attrs, "error", trace.Error.Error())
	}

	p.logger.Log(context.Background(), p.level, "request trace", attrs...)
}

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}
