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
		s.logger.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
		)
	})
}

// withAppKeyTracking extracts and validates the application key header,
// rejects requests when require_key is set and the key is absent,
// and injects the key into the request context.
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

	s.emitTrace(pctx, r, resp.StatusCode, false, nil)

	// Relay upstream response headers.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
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

