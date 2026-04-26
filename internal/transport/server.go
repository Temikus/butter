package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/temikus/butter/internal/appkey"
	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/proxy"
)

// Server is the HTTP transport layer for Butter.
type Server struct {
	httpServer     *http.Server
	engine         *proxy.Engine
	logger         *slog.Logger
	chain          *plugin.Chain
	metricsHandler http.Handler
	appKeyStore    *appkey.Store
	appKeyHeader   string
	appKeyRequire  bool
	appKeyTTL      time.Duration // default TTL applied to vended keys (0 = none)
}

// Option configures optional Server behavior.
type Option func(*Server)

// WithMetricsHandler registers an HTTP handler at GET /metrics.
func WithMetricsHandler(h http.Handler) Option {
	return func(s *Server) {
		s.metricsHandler = h
	}
}

// WithAppKeyStore enables application-key tracking with the given store.
// header is the request header name to read the key from.
// requireKey causes requests without a valid key to receive 400 Bad Request.
func WithAppKeyStore(store *appkey.Store, header string, requireKey bool) Option {
	return func(s *Server) {
		s.appKeyStore = store
		s.appKeyHeader = header
		s.appKeyRequire = requireKey
	}
}

// WithAppKeyDefaultTTL sets the default expiry applied to keys vended via
// POST /v1/app-keys when the request body does not specify ttl_seconds.
// A non-positive duration disables the default.
func WithAppKeyDefaultTTL(ttl time.Duration) Option {
	return func(s *Server) {
		if ttl > 0 {
			s.appKeyTTL = ttl
		}
	}
}

func NewServer(cfg *config.ServerConfig, engine *proxy.Engine, logger *slog.Logger, chain *plugin.Chain, opts ...Option) *Server {
	s := &Server{
		engine: engine,
		logger: logger,
		chain:  chain,
	}

	for _, opt := range opts {
		opt(s)
	}

	mux := http.NewServeMux()
	// Chat completions: optionally wrapped with app-key tracking middleware.
	var chatHandler http.Handler = http.HandlerFunc(s.handleChatCompletions)
	if s.appKeyStore != nil {
		chatHandler = s.withAppKeyTracking(chatHandler)
	}
	mux.Handle("POST /v1/chat/completions", chatHandler)
	var embeddingsHandler http.Handler = http.HandlerFunc(s.handleEmbeddings)
	if s.appKeyStore != nil {
		embeddingsHandler = s.withAppKeyTracking(embeddingsHandler)
	}
	mux.Handle("POST /v1/embeddings", embeddingsHandler)
	var messagesHandler http.Handler = http.HandlerFunc(s.handleAnthropicMessages)
	if s.appKeyStore != nil {
		messagesHandler = s.withAppKeyTracking(messagesHandler)
	}
	mux.Handle("POST /v1/messages", messagesHandler)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("/native/{provider}/{path...}", s.handleNativePassthrough)
	if s.metricsHandler != nil {
		mux.Handle("GET /metrics", s.metricsHandler)
	}
	if s.appKeyStore != nil {
		mux.HandleFunc("POST /v1/app-keys", s.handleAppKeyCreate)
		mux.HandleFunc("GET /v1/app-keys", s.handleAppKeyList)
		mux.HandleFunc("GET /v1/app-keys/{key}/usage", s.handleAppKeyUsage)
		mux.HandleFunc("DELETE /v1/app-keys/{key}", s.handleAppKeyRevoke)
		mux.HandleFunc("PATCH /v1/app-keys/{key}", s.handleAppKeyUpdate)
		mux.HandleFunc("POST /v1/app-keys/{key}/rotate", s.handleAppKeyRotate)
		mux.HandleFunc("GET /v1/usage", s.handleUsageAggregate)
	}

	s.httpServer = &http.Server{
		Addr:         cfg.Address,
		Handler:      s.withMiddleware(mux),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	return s
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("butter listening", "address", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying HTTP handler for use in tests.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		level := slog.LevelInfo
		if r.URL.Path == "/healthz" {
			level = slog.LevelDebug
		}
		s.logger.Log(r.Context(), level, "request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
		)
	})
}

// withAppKeyTracking extracts and validates the application key header,
// rejects requests when require_key is set and the key is absent,
// and injects the key into the request context. When the presented key is
// known to the store but inactive (revoked or expired), the request is
// rejected with 401 regardless of requireKey — silently degrading a
// presented-but-inactive key to anonymous would mask credential leaks.
func (s *Server) withAppKeyTracking(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(s.appKeyHeader)
		if key == "" {
			if s.appKeyRequire {
				s.writeError(w, http.StatusBadRequest, "missing required app key header: "+s.appKeyHeader)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !appkey.IsValid(key) {
			s.writeError(w, http.StatusBadRequest, "invalid app key format")
			return
		}
		if rec := s.appKeyStore.Lookup(key); rec != nil && !s.appKeyStore.IsActive(rec) {
			msg := "app key revoked"
			if rec.RevokedAt.Load() == 0 {
				msg = "app key expired"
			}
			s.writeError(w, http.StatusUnauthorized, msg)
			return
		}
		r = r.WithContext(appkey.WithKey(r.Context(), key))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "ok")
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	// Run transport pre-hooks.
	pctx := &plugin.RequestContext{
		Request:   r,
		Body:      body,
		Metadata:  make(map[string]any),
		StartTime: time.Now(),
	}
	injectAppKeyMetadata(r.Context(), pctx.Metadata)
	if s.chain != nil {
		s.chain.RunPreHTTP(pctx)
		if pctx.ShortCircuit {
			s.writeShortCircuit(w, pctx)
			return
		}
		body = pctx.Body
	}

	// Store pctx in request context so the engine can populate Provider/Model.
	r = r.WithContext(plugin.WithRequestContext(r.Context(), pctx))

	// Check if this is a streaming request by inspecting the raw body.
	if isStreamRequest(body) {
		s.handleStream(w, r, body, pctx)
		return
	}

	resp, err := s.engine.Dispatch(r.Context(), body)
	if err != nil {
		s.logger.Error("dispatch failed", "error", err)
		status := http.StatusBadGateway
		var pe *provider.ProviderError
		if errors.As(err, &pe) {
			status = pe.StatusCode
		}
		s.writeError(w, status, err.Error())
		s.emitTrace(pctx, r, status, false, err)
		return
	}

	// Run transport post-hooks.
	if s.chain != nil {
		s.chain.RunPostHTTP(pctx)
	}

	s.emitTrace(pctx, r, resp.StatusCode, false, nil)

	// Async usage tracking — never blocks the response path.
	if s.appKeyStore != nil {
		if key, ok := appkey.FromContext(r.Context()); ok {
			model := pctx.Model
			rawBody := resp.RawBody
			store := s.appKeyStore
			go func() {
				pt, ct := appkey.ExtractUsage(rawBody)
				store.RecordRequest(key, model, false, pt, ct)
			}()
		}
	}

	// Relay provider response headers, skipping headers that must be
	// recalculated by the HTTP server (Content-Length, Transfer-Encoding)
	// or that Butter sets explicitly (Content-Type).
	for k, vs := range resp.Headers {
		switch k {
		case "Content-Length", "Transfer-Encoding", "Content-Type":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.RawBody)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, body []byte, pctx *plugin.RequestContext) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	stream, err := s.engine.DispatchStream(r.Context(), body)
	if err != nil {
		s.logger.Error("stream dispatch failed", "error", err)
		status := http.StatusBadGateway
		var pe *provider.ProviderError
		if errors.As(err, &pe) {
			status = pe.StatusCode
		}
		s.writeError(w, status, err.Error())
		s.emitTrace(pctx, r, status, true, err)
		return
	}
	defer func() { _ = stream.Close() }()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var streamErr error
	for {
		chunk, err := stream.Next()
		if err != nil {
			if err == io.EOF {
				// Send the final [DONE] marker.
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				break
			}
			s.logger.Error("stream read error", "error", err)
			streamErr = err
			break
		}

		// Run stream chunk hooks.
		if s.chain != nil && pctx != nil {
			chunk = s.chain.RunStreamChunk(pctx, chunk)
		}

		// Write the SSE chunk and flush immediately.
		_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
		flusher.Flush()
	}

	s.emitTrace(pctx, r, http.StatusOK, true, streamErr)

	// Async usage tracking for streaming requests (token counts not extracted).
	if s.appKeyStore != nil {
		if key, ok := appkey.FromContext(r.Context()); ok {
			model := pctx.Model
			store := s.appKeyStore
			go store.RecordRequest(key, model, true, 0, 0)
		}
	}
}

// flushWriter wraps an io.Writer and http.Flusher, calling Flush after every
// Write. Used for streaming passthrough to relay SSE chunks immediately.
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if n > 0 {
		fw.flusher.Flush()
	}
	return n, err
}

func (s *Server) handleNativePassthrough(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	upstreamPath := "/" + r.PathValue("path")

	// Run transport pre-hooks.
	pctx := &plugin.RequestContext{
		Request:   r,
		Provider:  providerName,
		Metadata:  make(map[string]any),
		StartTime: time.Now(),
	}
	injectAppKeyMetadata(r.Context(), pctx.Metadata)
	if s.chain != nil {
		s.chain.RunPreHTTP(pctx)
		if pctx.ShortCircuit {
			s.writeShortCircuit(w, pctx)
			return
		}
	}

	// Clone headers, stripping hop-by-hop headers.
	fwdHeaders := r.Header.Clone()
	fwdHeaders.Del("Host")
	fwdHeaders.Del("Connection")

	resp, err := s.engine.DispatchPassthrough(r.Context(), providerName, r.Method, upstreamPath, r.Body, fwdHeaders)
	if err != nil {
		s.logger.Error("passthrough dispatch failed", "provider", providerName, "error", err)
		s.writeError(w, http.StatusBadGateway, err.Error())
		s.emitTrace(pctx, r, http.StatusBadGateway, false, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if s.chain != nil {
		s.chain.RunPostHTTP(pctx)
	}

	streaming := isSSEResponse(resp)
	s.emitTrace(pctx, r, resp.StatusCode, streaming, nil)

	// Relay upstream response headers.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// For SSE streaming responses, flush each chunk immediately so the client
	// receives events in real time instead of buffering until EOF.
	if streaming {
		flusher, ok := w.(http.Flusher)
		if !ok {
			s.logger.Warn("streaming passthrough: ResponseWriter does not support Flush")
			_, _ = io.Copy(w, resp.Body)
			return
		}
		_, _ = io.Copy(&flushWriter{w: w, flusher: flusher}, resp.Body)
	} else {
		_, _ = io.Copy(w, resp.Body)
	}
}

// handleAnthropicMessages handles POST /v1/messages — the Anthropic Messages API
// endpoint. Routes to providers via DispatchAnthropicNative with failover support.
func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Run transport pre-hooks.
	pctx := &plugin.RequestContext{
		Request:   r,
		Body:      body,
		Metadata:  make(map[string]any),
		StartTime: time.Now(),
	}
	injectAppKeyMetadata(r.Context(), pctx.Metadata)
	ctx := plugin.WithRequestContext(r.Context(), pctx)
	if s.chain != nil {
		s.chain.RunPreHTTP(pctx)
		if pctx.ShortCircuit {
			s.writeShortCircuit(w, pctx)
			return
		}
	}

	// Forward client headers, stripping hop-by-hop headers.
	fwdHeaders := r.Header.Clone()
	fwdHeaders.Del("Host")
	fwdHeaders.Del("Connection")

	resp, err := s.engine.DispatchAnthropicNative(ctx, body, fwdHeaders)
	if err != nil {
		status := http.StatusBadGateway
		var pe *provider.ProviderError
		if errors.As(err, &pe) {
			status = pe.StatusCode
		}
		s.logger.Error("anthropic native dispatch failed", "error", err)
		s.writeError(w, status, err.Error())
		s.emitTrace(pctx, r, status, false, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if s.chain != nil {
		s.chain.RunPostHTTP(pctx)
	}

	streaming := isSSEResponse(resp)
	s.emitTrace(pctx, r, resp.StatusCode, streaming, nil)

	// Relay upstream response headers.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	if streaming {
		flusher, ok := w.(http.Flusher)
		if ok {
			_, _ = io.Copy(&flushWriter{w: w, flusher: flusher}, resp.Body)
		} else {
			s.logger.Warn("streaming messages: ResponseWriter does not support Flush")
			_, _ = io.Copy(w, resp.Body)
		}

		// Async usage tracking for streaming requests (token counts not extracted;
		// matches /v1/chat/completions parity. TODO: tee message_delta/message_stop
		// SSE events to capture cumulative input/output tokens).
		if s.appKeyStore != nil {
			if key, ok := appkey.FromContext(r.Context()); ok {
				model := pctx.Model
				store := s.appKeyStore
				go store.RecordRequest(key, model, true, 0, 0)
			}
		}
	} else {
		// Buffer the upstream body so we can extract usage tokens after relay.
		// 16 MB cap defends against runaway responses; logged on truncation.
		const maxBody = 16 << 20
		rawBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			s.logger.Error("failed to buffer messages response", "error", err)
			return
		}
		_, _ = w.Write(rawBody)

		if s.appKeyStore != nil {
			if key, ok := appkey.FromContext(r.Context()); ok {
				model := pctx.Model
				store := s.appKeyStore
				go func() {
					in, out := appkey.ExtractAnthropicUsage(rawBody)
					store.RecordRequest(key, model, false, in, out)
				}()
			}
		}
	}
}

// emitTrace sends a RequestTrace to observability plugins via the chain.
// pctx.Metadata is merged into the trace so that plugins can carry state
// (e.g. an OTel span stashed in PreHTTP) through to OnTrace.
// Built-in keys ("method", "path", "streaming") take precedence.
func (s *Server) emitTrace(pctx *plugin.RequestContext, r *http.Request, status int, streaming bool, err error) {
	if s.chain == nil {
		return
	}
	meta := make(map[string]any, len(pctx.Metadata)+3)
	for k, v := range pctx.Metadata {
		meta[k] = v
	}
	meta["method"] = r.Method
	meta["path"] = r.URL.Path
	meta["streaming"] = streaming

	trace := &plugin.RequestTrace{
		Provider:   pctx.Provider,
		Model:      pctx.Model,
		StatusCode: status,
		Duration:   time.Since(pctx.StartTime),
		Error:      err,
		Metadata:   meta,
	}
	s.chain.EmitTrace(trace)
}

func (s *Server) writeShortCircuit(w http.ResponseWriter, pctx *plugin.RequestContext) {
	status := pctx.ShortCircuitStatus
	if status == 0 {
		status = http.StatusForbidden
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if len(pctx.ShortCircuitBody) > 0 {
		_, _ = w.Write(pctx.ShortCircuitBody)
	}
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	// Run transport pre-hooks.
	pctx := &plugin.RequestContext{
		Request:   r,
		Body:      body,
		Metadata:  make(map[string]any),
		StartTime: time.Now(),
	}
	injectAppKeyMetadata(r.Context(), pctx.Metadata)
	if s.chain != nil {
		s.chain.RunPreHTTP(pctx)
		if pctx.ShortCircuit {
			s.writeShortCircuit(w, pctx)
			return
		}
		body = pctx.Body
	}

	resp, err := s.engine.DispatchEmbeddings(r.Context(), body)
	if err != nil {
		s.logger.Error("embeddings dispatch failed", "error", err)
		status := http.StatusBadGateway
		var pe *provider.ProviderError
		if errors.As(err, &pe) {
			status = pe.StatusCode
		}
		s.writeError(w, status, err.Error())
		return
	}

	// Run transport post-hooks.
	if s.chain != nil {
		s.chain.RunPostHTTP(pctx)
	}

	for k, vs := range resp.Headers {
		switch k {
		case "Content-Length", "Transfer-Encoding", "Content-Type":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.RawBody)
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	result := s.engine.ListModels()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"proxy_error"}}`, msg)
}

// isStreamRequest checks if the request body contains "stream": true.
// Uses bytes.Contains for a fast check that avoids full JSON parsing.
func isStreamRequest(body []byte) bool {
	return bytes.Contains(body, []byte(`"stream":true`)) ||
		bytes.Contains(body, []byte(`"stream": true`))
}

// injectAppKeyMetadata copies the resolved app key from the request context
// into the plugin metadata map, making it available to all plugins.
func injectAppKeyMetadata(ctx context.Context, metadata map[string]any) {
	if key, ok := appkey.FromContext(ctx); ok {
		metadata["app_key"] = key
	}
}

// isSSEResponse checks if the upstream response has a Content-Type indicating
// server-sent events, which requires per-chunk flushing for real-time delivery.
func isSSEResponse(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
}

