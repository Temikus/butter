package requestlog

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/temikus/butter/internal/plugin"
)

func TestPluginName(t *testing.T) {
	p := New(slog.Default())
	if p.Name() != "requestlog" {
		t.Fatalf("expected name %q, got %q", "requestlog", p.Name())
	}
}

func TestPluginClose(t *testing.T) {
	p := New(slog.Default())
	if err := p.Close(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestPluginInitDefaults(t *testing.T) {
	p := New(slog.Default())
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init(nil) failed: %v", err)
	}
	if p.level != slog.LevelInfo {
		t.Errorf("expected default level Info, got %v", p.level)
	}
	if p.logBodies {
		t.Error("expected logBodies=false by default")
	}
	if p.bodyMaxBytes != 1024 {
		t.Errorf("expected bodyMaxBytes=1024, got %d", p.bodyMaxBytes)
	}
}

func TestPluginInitCustomConfig(t *testing.T) {
	p := New(slog.Default())
	err := p.Init(map[string]any{
		"level":          "debug",
		"log_bodies":     true,
		"body_max_bytes": 512,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if p.level != slog.LevelDebug {
		t.Errorf("expected level Debug, got %v", p.level)
	}
	if !p.logBodies {
		t.Error("expected logBodies=true")
	}
	if p.bodyMaxBytes != 512 {
		t.Errorf("expected bodyMaxBytes=512, got %d", p.bodyMaxBytes)
	}
}

func TestPluginInitLevels(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"unknown", slog.LevelInfo},
	}
	for _, tt := range tests {
		p := New(slog.Default())
		_ = p.Init(map[string]any{"level": tt.input})
		if p.level != tt.want {
			t.Errorf("level %q: expected %v, got %v", tt.input, tt.want, p.level)
		}
	}
}

func TestOnTraceLogsFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(nil)

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   150 * time.Millisecond,
		Metadata: map[string]any{
			"method":    "POST",
			"path":      "/v1/chat/completions",
			"streaming": false,
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	for _, want := range []string{
		`"provider":"openai"`,
		`"model":"gpt-4o"`,
		`"status":200`,
		`"duration_ms":150`,
		`"method":"POST"`,
		`"path":"/v1/chat/completions"`,
		`"streaming":false`,
	} {
		if !bytes.Contains([]byte(output), []byte(want)) {
			t.Errorf("output missing %s\ngot: %s", want, output)
		}
	}
}

func TestOnTraceWithError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(nil)

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 502,
		Duration:   50 * time.Millisecond,
		Error:      errTest("upstream timeout"),
		Metadata:   map[string]any{},
	}
	p.OnTrace(trace)

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte(`"error":"upstream timeout"`)) {
		t.Errorf("output missing error field\ngot: %s", output)
	}
}

func TestOnTraceBodiesLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(map[string]any{"log_bodies": true})

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Metadata: map[string]any{
			"request_body":  []byte(`{"model":"gpt-4o"}`),
			"response_body": []byte(`{"choices":[]}`),
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("request_body")) {
		t.Errorf("output missing request_body\ngot: %s", output)
	}
	if !bytes.Contains([]byte(output), []byte("response_body")) {
		t.Errorf("output missing response_body\ngot: %s", output)
	}
}

func TestOnTraceBodiesNotLoggedByDefault(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(nil)

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Metadata: map[string]any{
			"request_body":  []byte(`{"model":"gpt-4o"}`),
			"response_body": []byte(`{"choices":[]}`),
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	if bytes.Contains([]byte(output), []byte("request_body")) {
		t.Error("bodies should not be logged when log_bodies=false")
	}
}

func TestOnTraceBodyTruncation(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(map[string]any{"log_bodies": true, "body_max_bytes": 10})

	longBody := make([]byte, 100)
	for i := range longBody {
		longBody[i] = 'x'
	}

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Metadata: map[string]any{
			"request_body": longBody,
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("truncated")) {
		t.Errorf("expected truncation marker in output\ngot: %s", output)
	}
}

// errTest is a simple error type for tests.
type errTest string

func (e errTest) Error() string { return string(e) }
