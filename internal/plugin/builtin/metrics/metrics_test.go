package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/temikus/butter/internal/plugin"
)

func TestPluginName(t *testing.T) {
	p := New()
	if p.Name() != "metrics" {
		t.Fatalf("expected name %q, got %q", "metrics", p.Name())
	}
}

func TestInitCreatesHandler(t *testing.T) {
	p := New()
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	if p.Handler() == nil {
		t.Fatal("expected non-nil handler after Init")
	}
}

func TestOnTraceRecordsMetrics(t *testing.T) {
	p := New()
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	// Emit a successful trace.
	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   150 * time.Millisecond,
		Metadata:   map[string]any{"streaming": false},
	})

	// Emit an error trace.
	p.OnTrace(&plugin.RequestTrace{
		Provider:   "anthropic",
		Model:      "claude-sonnet-4-20250514",
		StatusCode: 500,
		Duration:   50 * time.Millisecond,
		Error:      io.ErrUnexpectedEOF,
		Metadata:   map[string]any{"streaming": true},
	})

	// Scrape the metrics endpoint.
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify request total counter exists with correct labels.
	if !strings.Contains(body, "butter_request_total") {
		t.Error("missing butter_request_total metric")
	}
	if !strings.Contains(body, `provider="openai"`) {
		t.Error("missing openai provider label")
	}
	if !strings.Contains(body, `provider="anthropic"`) {
		t.Error("missing anthropic provider label")
	}

	// Verify duration histogram.
	if !strings.Contains(body, "butter_request_duration") {
		t.Error("missing butter_request_duration metric")
	}

	// Verify error counter exists and has the error trace.
	if !strings.Contains(body, "butter_request_errors") {
		t.Error("missing butter_request_errors metric")
	}
}

func TestOnTraceNoMetadataStreaming(t *testing.T) {
	p := New()
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	// Trace with nil metadata — should not panic.
	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openrouter",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `streaming="false"`) {
		t.Error("expected streaming=false when metadata is nil")
	}
}

func TestOnTraceWithPerKeyMetrics(t *testing.T) {
	p := New()
	if err := p.Init(map[string]any{"per_key_metrics": true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
		Metadata:   map[string]any{"streaming": false, "app_key": "btr_abc123"},
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, `app_key="btr_abc123"`) {
		t.Error("expected app_key label with value btr_abc123")
	}
}

func TestOnTracePerKeyMetricsNoAppKey(t *testing.T) {
	p := New()
	if err := p.Init(map[string]any{"per_key_metrics": true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
		Metadata:   map[string]any{"streaming": false},
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, `app_key=""`) {
		t.Error("expected app_key label with empty value when no app key present")
	}
}

func TestOnTraceWithoutPerKeyMetrics(t *testing.T) {
	p := New()
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
		Metadata:   map[string]any{"streaming": false, "app_key": "btr_abc123"},
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	if strings.Contains(body, "app_key") {
		t.Error("expected NO app_key label when per_key_metrics is disabled")
	}
}

func TestOnTracePerKeyMetricsMultipleKeys(t *testing.T) {
	p := New()
	if err := p.Init(map[string]any{"per_key_metrics": true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Close() }()

	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   100 * time.Millisecond,
		Metadata:   map[string]any{"streaming": false, "app_key": "btr_key1"},
	})
	p.OnTrace(&plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   50 * time.Millisecond,
		Metadata:   map[string]any{"streaming": false, "app_key": "btr_key2"},
	})

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))

	body := rec.Body.String()
	if !strings.Contains(body, `app_key="btr_key1"`) {
		t.Error("expected app_key label with value btr_key1")
	}
	if !strings.Contains(body, `app_key="btr_key2"`) {
		t.Error("expected app_key label with value btr_key2")
	}
}

func TestCloseShutsMeterProvider(t *testing.T) {
	p := New()
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Closing again should be safe (meterProvider is already shut down,
	// but Close is idempotent in the SDK).
}

func TestCloseWithoutInit(t *testing.T) {
	p := New()
	// Close without Init — should not panic or error.
	if err := p.Close(); err != nil {
		t.Fatalf("Close without Init: %v", err)
	}
}
