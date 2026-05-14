package requestlog

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	if p.requestIDHeader != "X-Request-Id" {
		t.Errorf("expected requestIDHeader=%q, got %q", "X-Request-Id", p.requestIDHeader)
	}
	if !p.logAppKey {
		t.Error("expected logAppKey=true by default")
	}
}

func TestPluginInitCustomConfig(t *testing.T) {
	p := New(slog.Default())
	err := p.Init(map[string]any{
		"level":             "debug",
		"log_bodies":        true,
		"body_max_bytes":    512,
		"request_id_header": "X-Correlation-Id",
		"log_app_key":       false,
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
	if p.requestIDHeader != "X-Correlation-Id" {
		t.Errorf("expected requestIDHeader=%q, got %q", "X-Correlation-Id", p.requestIDHeader)
	}
	if p.logAppKey {
		t.Error("expected logAppKey=false")
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

// --- PreHTTP tests ---

func newPctx(r *http.Request) *plugin.RequestContext {
	return &plugin.RequestContext{
		Request:   r,
		Body:      []byte(`{"model":"gpt-4o"}`),
		Metadata:  make(map[string]any),
		StartTime: time.Now(),
	}
}

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestPreHTTPGeneratesRequestID(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(nil)

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	if err := p.PreHTTP(pctx); err != nil {
		t.Fatalf("PreHTTP failed: %v", err)
	}

	reqID, ok := pctx.Metadata[metaRequestID].(string)
	if !ok || reqID == "" {
		t.Fatal("expected request_id in metadata")
	}
	if !uuidV4Pattern.MatchString(reqID) {
		t.Errorf("request_id %q does not match UUID v4 pattern", reqID)
	}
}

func TestPreHTTPUsesClientRequestID(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(nil)

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Request-Id", "client-provided-id")
	pctx := newPctx(r)
	if err := p.PreHTTP(pctx); err != nil {
		t.Fatalf("PreHTTP failed: %v", err)
	}

	reqID := pctx.Metadata[metaRequestID].(string)
	if reqID != "client-provided-id" {
		t.Errorf("expected client-provided-id, got %q", reqID)
	}
}

func TestPreHTTPSetsResponseHeader(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(nil)

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	if pctx.ResponseHeaders == nil {
		t.Fatal("expected ResponseHeaders to be initialized")
	}
	reqID := pctx.Metadata[metaRequestID].(string)
	headerVal := pctx.ResponseHeaders.Get("X-Request-Id")
	if headerVal != reqID {
		t.Errorf("response header %q does not match metadata request_id %q", headerVal, reqID)
	}
}

func TestPreHTTPCustomHeaderName(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"request_id_header": "X-Correlation-Id"})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Correlation-Id", "custom-corr-id")
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	reqID := pctx.Metadata[metaRequestID].(string)
	if reqID != "custom-corr-id" {
		t.Errorf("expected custom-corr-id, got %q", reqID)
	}
	headerVal := pctx.ResponseHeaders.Get("X-Correlation-Id")
	if headerVal != "custom-corr-id" {
		t.Errorf("expected X-Correlation-Id response header, got %q", headerVal)
	}
}

func TestPreHTTPStashesRequestBody(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"log_bodies": true})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	body, ok := pctx.Metadata[metaRequestBody].([]byte)
	if !ok {
		t.Fatal("expected request_body in metadata")
	}
	if string(body) != `{"model":"gpt-4o"}` {
		t.Errorf("unexpected request body: %s", body)
	}
}

func TestPreHTTPAllocatesStreamBuffer(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"log_bodies": true})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	if _, ok := pctx.Metadata[metaStreamBodyBuf].(*cappedWriter); !ok {
		t.Fatal("expected _requestlog_buf cappedWriter in metadata")
	}
	if _, ok := pctx.Metadata[metaStreamSink].(*cappedWriter); !ok {
		t.Fatal("expected _stream_body_sink cappedWriter in metadata")
	}
}

func TestPreHTTPNoBufferWithoutLogBodies(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(nil) // log_bodies defaults to false

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	if _, ok := pctx.Metadata[metaStreamBodyBuf]; ok {
		t.Error("expected no stream buffer when log_bodies=false")
	}
}

// --- StreamChunk tests ---

func TestStreamChunkAccumulatesBody(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"log_bodies": true, "body_max_bytes": 4096})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	chunks := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n",
	}
	for _, c := range chunks {
		out, err := p.StreamChunk(pctx, []byte(c))
		if err != nil {
			t.Fatalf("StreamChunk error: %v", err)
		}
		if string(out) != c {
			t.Errorf("StreamChunk modified chunk: got %q, want %q", out, c)
		}
	}

	cw := pctx.Metadata[metaStreamBodyBuf].(*cappedWriter)
	got := string(*cw.buf)
	want := chunks[0] + chunks[1]
	if got != want {
		t.Errorf("accumulated body:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestStreamChunkRespectsBodyMaxBytes(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"log_bodies": true, "body_max_bytes": 10})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	chunk := []byte("this is a very long chunk that exceeds the cap")
	out, err := p.StreamChunk(pctx, chunk)
	if err != nil {
		t.Fatalf("StreamChunk error: %v", err)
	}
	if string(out) != string(chunk) {
		t.Error("StreamChunk should not modify the chunk")
	}

	cw := pctx.Metadata[metaStreamBodyBuf].(*cappedWriter)
	if len(*cw.buf) != 10 {
		t.Errorf("expected buffer capped at 10 bytes, got %d", len(*cw.buf))
	}
}

func TestStreamChunkNoopWithoutBuffer(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(nil)

	pctx := &plugin.RequestContext{Metadata: make(map[string]any)}
	out, err := p.StreamChunk(pctx, []byte("data: test\n"))
	if err != nil {
		t.Fatalf("StreamChunk error: %v", err)
	}
	if string(out) != "data: test\n" {
		t.Error("StreamChunk should pass through chunk unchanged")
	}
}

// --- PostHTTP tests ---

func TestPostHTTPTransfersBuffer(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"log_bodies": true, "body_max_bytes": 4096})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	_, _ = p.StreamChunk(pctx, []byte("chunk1"))
	_, _ = p.StreamChunk(pctx, []byte("chunk2"))

	_ = p.PostHTTP(pctx)

	body, ok := pctx.Metadata[metaResponseBody].([]byte)
	if !ok {
		t.Fatal("expected response_body in metadata after PostHTTP")
	}
	if string(body) != "chunk1chunk2" {
		t.Errorf("expected %q, got %q", "chunk1chunk2", body)
	}

	if _, ok := pctx.Metadata[metaStreamBodyBuf]; ok {
		t.Error("expected _requestlog_buf to be cleaned up")
	}
	if _, ok := pctx.Metadata[metaStreamSink]; ok {
		t.Error("expected _stream_body_sink to be cleaned up")
	}
}

func TestPostHTTPPreservesExistingResponseBody(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(map[string]any{"log_bodies": true, "body_max_bytes": 4096})

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	pctx := newPctx(r)
	_ = p.PreHTTP(pctx)

	pctx.Metadata[metaResponseBody] = []byte(`{"choices":[]}`)

	_ = p.PostHTTP(pctx)

	body := pctx.Metadata[metaResponseBody].([]byte)
	if string(body) != `{"choices":[]}` {
		t.Errorf("PostHTTP overwrote existing response_body: got %q", body)
	}
}

func TestPostHTTPNoopWithoutBuffer(t *testing.T) {
	p := New(slog.Default())
	_ = p.Init(nil)

	pctx := &plugin.RequestContext{Metadata: make(map[string]any)}
	if err := p.PostHTTP(pctx); err != nil {
		t.Fatalf("PostHTTP error: %v", err)
	}
}

// --- OnTrace tests ---

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

func TestOnTraceIncludesRequestID(t *testing.T) {
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
			metaRequestID: "test-uuid-1234",
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte(`"request_id":"test-uuid-1234"`)) {
		t.Errorf("output missing request_id\ngot: %s", output)
	}
}

func TestOnTraceIncludesAppKey(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(nil) // logAppKey defaults to true

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Metadata: map[string]any{
			metaAppKey: "btr_test123456789012",
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte(`"app_key":"btr_test123456789012"`)) {
		t.Errorf("output missing app_key\ngot: %s", output)
	}
}

func TestOnTraceOmitsAppKeyWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := New(logger)
	_ = p.Init(map[string]any{"log_app_key": false})

	trace := &plugin.RequestTrace{
		Provider:   "openai",
		Model:      "gpt-4o",
		StatusCode: 200,
		Duration:   10 * time.Millisecond,
		Metadata: map[string]any{
			metaAppKey: "btr_test123456789012",
		},
	}
	p.OnTrace(trace)

	output := buf.String()
	if bytes.Contains([]byte(output), []byte("app_key")) {
		t.Errorf("app_key should not be logged when log_app_key=false\ngot: %s", output)
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

// --- cappedWriter tests ---

func TestCappedWriterNeverErrors(t *testing.T) {
	buf := make([]byte, 0, 10)
	cw := &cappedWriter{buf: &buf, max: 5}

	for i := 0; i < 10; i++ {
		n, err := cw.Write([]byte("abcde"))
		if err != nil {
			t.Fatalf("write %d returned error: %v", i, err)
		}
		if n != 5 {
			t.Fatalf("write %d returned n=%d, want 5", i, n)
		}
	}

	if len(*cw.buf) != 5 {
		t.Errorf("buffer should be capped at 5, got %d", len(*cw.buf))
	}
}

func TestCappedWriterPartialFill(t *testing.T) {
	buf := make([]byte, 0, 20)
	cw := &cappedWriter{buf: &buf, max: 8}

	_, _ = cw.Write([]byte("abc"))
	_, _ = cw.Write([]byte("defghijk"))

	if len(*cw.buf) != 8 {
		t.Errorf("expected 8 bytes, got %d", len(*cw.buf))
	}
	if string(*cw.buf) != "abcdefgh" {
		t.Errorf("expected %q, got %q", "abcdefgh", *cw.buf)
	}
}

// --- UUID tests ---

func TestUUIDFormat(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := generateUUID()
		if !uuidV4Pattern.MatchString(id) {
			t.Fatalf("generated UUID %q does not match v4 pattern", id)
		}
	}
}

func TestUUIDUniqueness(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := generateUUID()
		if seen[id] {
			t.Fatalf("duplicate UUID: %s", id)
		}
		seen[id] = true
	}
}

// errTest is a simple error type for tests.
type errTest string

func (e errTest) Error() string { return string(e) }
