package requestlog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/temikus/butter/internal/plugin"
)

const (
	metaRequestID     = "request_id"
	metaRequestBody   = "request_body"
	metaResponseBody  = "response_body"
	metaAppKey        = "app_key"
	metaStreamBodyBuf = "_requestlog_buf"
	metaStreamSink    = plugin.MetaStreamBodySink
)

var (
	uuidFallbackMu  sync.Mutex
	uuidFallbackSeq uint64
)

// Plugin logs completed request traces via slog. Implements both
// plugin.TransportPlugin (for correlation IDs and streaming body
// capture) and plugin.ObservabilityPlugin (for structured logging).
type Plugin struct {
	logger          *slog.Logger
	level           slog.Level
	logBodies       bool
	bodyMaxBytes    int
	requestIDHeader string
	logAppKey       bool
	bufPool         sync.Pool
}

// New creates a request logging plugin that uses the given logger.
func New(logger *slog.Logger) *Plugin {
	p := &Plugin{
		logger:          logger,
		level:           slog.LevelInfo,
		bodyMaxBytes:    1024,
		requestIDHeader: "X-Request-Id",
		logAppKey:       true,
	}
	p.bufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 4096)
			return &b
		},
	}
	return p
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
	if v, ok := cfg["request_id_header"].(string); ok && v != "" {
		p.requestIDHeader = v
	}
	if v, ok := cfg["log_app_key"].(bool); ok {
		p.logAppKey = v
	}
	return nil
}

func (p *Plugin) Close() error { return nil }

// --- TransportPlugin implementation ---

// PreHTTP generates (or extracts) a correlation ID and optionally sets up
// a streaming body buffer for response capture.
func (p *Plugin) PreHTTP(pctx *plugin.RequestContext) error {
	reqID := ""
	if pctx.Request != nil {
		reqID = pctx.Request.Header.Get(p.requestIDHeader)
	}
	if reqID == "" {
		reqID = generateUUID()
	}
	pctx.Metadata[metaRequestID] = reqID

	if pctx.ResponseHeaders == nil {
		pctx.ResponseHeaders = make(http.Header)
	}
	pctx.ResponseHeaders.Set(p.requestIDHeader, reqID)

	if p.logBodies {
		pctx.Metadata[metaRequestBody] = pctx.Body

		bufp := p.bufPool.Get().(*[]byte)
		*bufp = (*bufp)[:0]
		cw := &cappedWriter{buf: bufp, max: p.bodyMaxBytes}
		pctx.Metadata[metaStreamBodyBuf] = cw
		pctx.Metadata[metaStreamSink] = cw
	}

	return nil
}

// PostHTTP transfers the accumulated streaming body buffer into metadata
// so OnTrace can log it, then returns the buffer to the pool.
func (p *Plugin) PostHTTP(pctx *plugin.RequestContext) error {
	cw, ok := pctx.Metadata[metaStreamBodyBuf].(*cappedWriter)
	if !ok || cw == nil {
		return nil
	}

	if len(*cw.buf) > 0 {
		// Only overwrite if there isn't already a response body (non-streaming
		// paths populate this from the full response).
		if _, exists := pctx.Metadata[metaResponseBody]; !exists {
			dst := make([]byte, len(*cw.buf))
			copy(dst, *cw.buf)
			pctx.Metadata[metaResponseBody] = dst
		}
	}

	*cw.buf = (*cw.buf)[:0]
	p.bufPool.Put(cw.buf)
	delete(pctx.Metadata, metaStreamBodyBuf)
	delete(pctx.Metadata, metaStreamSink)

	return nil
}

// StreamChunk appends each SSE chunk to the body buffer (if active).
func (p *Plugin) StreamChunk(pctx *plugin.RequestContext, chunk []byte) ([]byte, error) {
	if cw, ok := pctx.Metadata[metaStreamBodyBuf].(*cappedWriter); ok && cw != nil {
		_, _ = cw.Write(chunk)
	}
	return chunk, nil
}

// --- ObservabilityPlugin implementation ---

// OnTrace logs a structured line for each completed request.
func (p *Plugin) OnTrace(trace *plugin.RequestTrace) {
	attrs := []any{
		"provider", trace.Provider,
		"model", trace.Model,
		"status", trace.StatusCode,
		"duration_ms", trace.Duration.Milliseconds(),
	}

	if trace.Metadata != nil {
		if v, ok := trace.Metadata[metaRequestID].(string); ok {
			attrs = append(attrs, metaRequestID, v)
		}
		if v, ok := trace.Metadata["method"].(string); ok {
			attrs = append(attrs, "method", v)
		}
		if v, ok := trace.Metadata["path"].(string); ok {
			attrs = append(attrs, "path", v)
		}
		if v, ok := trace.Metadata["streaming"].(bool); ok {
			attrs = append(attrs, "streaming", v)
		}
		if p.logAppKey {
			if v, ok := trace.Metadata[metaAppKey].(string); ok {
				attrs = append(attrs, metaAppKey, v)
			}
		}
		if p.logBodies {
			if v, ok := trace.Metadata[metaRequestBody].([]byte); ok {
				attrs = append(attrs, metaRequestBody, truncate(v, p.bodyMaxBytes))
			}
			if v, ok := trace.Metadata[metaResponseBody].([]byte); ok {
				attrs = append(attrs, metaResponseBody, truncate(v, p.bodyMaxBytes))
			}
		}
	}

	if trace.Error != nil {
		attrs = append(attrs, "error", trace.Error.Error())
	}

	p.logger.Log(context.Background(), p.level, "request trace", attrs...)
}

// --- Helpers ---

// cappedWriter accumulates bytes up to a maximum. Writes beyond the cap
// are silently discarded. Write never returns an error so it composes
// safely under io.MultiWriter without breaking a streaming relay.
//
// Not safe for concurrent use — relies on the plugin chain guarantee that
// StreamChunk and the Anthropic io.Copy path invoke Write serially on a
// single request goroutine.
type cappedWriter struct {
	buf *[]byte
	max int
}

func (cw *cappedWriter) Write(p []byte) (int, error) {
	remaining := cw.max - len(*cw.buf)
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		*cw.buf = append(*cw.buf, p[:remaining]...)
		return len(p), nil
	}
	*cw.buf = append(*cw.buf, p...)
	return len(p), nil
}

func truncate(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// generateUUID produces a random UUID v4 string. Falls back to a
// timestamp+counter hex identifier if the system entropy pool is
// unavailable. The fallback is deliberately not UUID-formatted to
// avoid confusing downstream validators.
func generateUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		uuidFallbackMu.Lock()
		uuidFallbackSeq++
		seq := uuidFallbackSeq
		uuidFallbackMu.Unlock()
		return fmt.Sprintf("%016x%08x", time.Now().UnixNano(), seq)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(buf[0:4]),
		hex.EncodeToString(buf[4:6]),
		hex.EncodeToString(buf[6:8]),
		hex.EncodeToString(buf[8:10]),
		hex.EncodeToString(buf[10:16]),
	)
}
