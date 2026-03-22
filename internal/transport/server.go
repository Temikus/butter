package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/proxy"
)

// Server is the HTTP transport layer for Butter.
type Server struct {
	httpServer *http.Server
	engine     *proxy.Engine
	logger     *slog.Logger
	chain      *plugin.Chain
}

func NewServer(cfg *config.ServerConfig, engine *proxy.Engine, logger *slog.Logger, chain *plugin.Chain) *Server {
	s := &Server{
		engine: engine,
		logger: logger,
		chain:  chain,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("/native/{provider}/{path...}", s.handleNativePassthrough)

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

	// Relay provider response headers.
	for k, vs := range resp.Headers {
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
func (s *Server) emitTrace(pctx *plugin.RequestContext, r *http.Request, status int, streaming bool, err error) {
	if s.chain == nil {
		return
	}
	trace := &plugin.RequestTrace{
		Provider:   pctx.Provider,
		Model:      pctx.Model,
		StatusCode: status,
		Duration:   time.Since(pctx.StartTime),
		Error:      err,
		Metadata: map[string]any{
			"method":    r.Method,
			"path":      r.URL.Path,
			"streaming": streaming,
		},
	}
	s.chain.EmitTrace(trace)
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

