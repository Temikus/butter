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
	"github.com/temikus/butter/internal/provider"
	"github.com/temikus/butter/internal/proxy"
)

// Server is the HTTP transport layer for Butter.
type Server struct {
	httpServer *http.Server
	engine     *proxy.Engine
	logger     *slog.Logger
}

func NewServer(cfg *config.ServerConfig, engine *proxy.Engine, logger *slog.Logger) *Server {
	s := &Server{
		engine: engine,
		logger: logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

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
	fmt.Fprint(w, "ok")
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	// Check if this is a streaming request by inspecting the raw body.
	if isStreamRequest(body) {
		s.handleStream(w, r, body)
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
		return
	}

	// Relay provider response headers.
	for k, vs := range resp.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(resp.RawBody)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, body []byte) {
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
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		chunk, err := stream.Next()
		if err != nil {
			if err == io.EOF {
				// Send the final [DONE] marker.
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			s.logger.Error("stream read error", "error", err)
			return
		}

		// Write the SSE chunk and flush immediately.
		fmt.Fprintf(w, "%s\n\n", chunk)
		flusher.Flush()
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"message":%q,"type":"proxy_error"}}`, msg)
}

// isStreamRequest checks if the request body contains "stream": true.
// Uses bytes.Contains for a fast check that avoids full JSON parsing.
func isStreamRequest(body []byte) bool {
	return bytes.Contains(body, []byte(`"stream":true`)) ||
		bytes.Contains(body, []byte(`"stream": true`))
}

