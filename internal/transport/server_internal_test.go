package transport

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temikus/butter/internal/config"
)

// TestNewServerAppliesHardeningConfig asserts the slowloris-relevant fields
// reach the http.Server from config, and that a zero value falls back to the
// documented default rather than to net/http's implicit "no limit".
func TestNewServerAppliesHardeningConfig(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	t.Run("from config", func(t *testing.T) {
		cfg := &config.ServerConfig{
			Address:           ":9999",
			ReadTimeout:       7 * time.Second,
			WriteTimeout:      11 * time.Second,
			ReadHeaderTimeout: 3 * time.Second,
			IdleTimeout:       17 * time.Second,
			MaxHeaderBytes:    4096,
		}
		s := NewServer(cfg, nil, logger, nil)

		if got := s.httpServer.ReadHeaderTimeout; got != 3*time.Second {
			t.Errorf("ReadHeaderTimeout = %v, want 3s", got)
		}
		if got := s.httpServer.IdleTimeout; got != 17*time.Second {
			t.Errorf("IdleTimeout = %v, want 17s", got)
		}
		if got := s.httpServer.MaxHeaderBytes; got != 4096 {
			t.Errorf("MaxHeaderBytes = %d, want 4096", got)
		}
		if got := s.httpServer.ReadTimeout; got != 7*time.Second {
			t.Errorf("ReadTimeout = %v, want 7s", got)
		}
		if got := s.httpServer.WriteTimeout; got != 11*time.Second {
			t.Errorf("WriteTimeout = %v, want 11s", got)
		}
		if s.writeTimeout != 11*time.Second {
			t.Errorf("streaming write window = %v, want 11s", s.writeTimeout)
		}
	})

	t.Run("defaults when unset", func(t *testing.T) {
		s := NewServer(&config.ServerConfig{Address: ":0"}, nil, logger, nil)

		if got := s.httpServer.ReadHeaderTimeout; got != config.DefaultReadHeaderTimeout {
			t.Errorf("ReadHeaderTimeout = %v, want %v", got, config.DefaultReadHeaderTimeout)
		}
		if got := s.httpServer.IdleTimeout; got != config.DefaultIdleTimeout {
			t.Errorf("IdleTimeout = %v, want %v", got, config.DefaultIdleTimeout)
		}
		if got := s.httpServer.MaxHeaderBytes; got != config.DefaultMaxHeaderBytes {
			t.Errorf("MaxHeaderBytes = %d, want %d", got, config.DefaultMaxHeaderBytes)
		}
	})
}

// TestStreamDeadlineDisabledWithoutWriteTimeout keeps the extender inert when
// the operator has turned WriteTimeout off, so no deadline is imposed where
// none was configured.
func TestStreamDeadlineDisabledWithoutWriteTimeout(t *testing.T) {
	s := &Server{writeTimeout: 0}
	if sd := s.newStreamDeadline(nil); !sd.disabled {
		t.Error("expected deadline extender to be disabled with WriteTimeout=0")
	}
}

// TestStreamDeadlineLogsOnceWhenUnsupported covers the debuggability path: a
// ResponseWriter that cannot carry a deadline silently reverts streams to
// being cut off at WriteTimeout, so the self-disable must leave a trace — and
// exactly one, not one per chunk.
func TestStreamDeadlineLogsOnceWhenUnsupported(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	s := &Server{writeTimeout: time.Second, logger: logger}

	// httptest.ResponseRecorder implements neither SetWriteDeadline nor
	// Unwrap, so the controller cannot reach a connection.
	sd := s.newStreamDeadline(httptest.NewRecorder())
	if !sd.disabled {
		t.Fatal("expected extender to disable itself on an unsupported writer")
	}
	for i := 0; i < 5; i++ {
		sd.extend()
	}

	if got := strings.Count(buf.String(), "write deadline extension unsupported"); got != 1 {
		t.Errorf("expected exactly 1 warning, got %d:\n%s", got, buf.String())
	}
}
