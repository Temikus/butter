package plugin

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
)

// stubPlugin implements Plugin only.
type stubPlugin struct {
	name      string
	initCfg   map[string]any
	initErr   error
	closeErr  error
	closed    bool
	closeOrder *[]string
}

func (s *stubPlugin) Name() string                  { return s.name }
func (s *stubPlugin) Init(cfg map[string]any) error  { s.initCfg = cfg; return s.initErr }
func (s *stubPlugin) Close() error {
	s.closed = true
	if s.closeOrder != nil {
		*s.closeOrder = append(*s.closeOrder, s.name)
	}
	return s.closeErr
}

// multiPlugin implements TransportPlugin, LLMPlugin, and ObservabilityPlugin.
type multiPlugin struct {
	stubPlugin
}

func (m *multiPlugin) PreHTTP(ctx *RequestContext) error                         { return nil }
func (m *multiPlugin) PostHTTP(ctx *RequestContext) error                        { return nil }
func (m *multiPlugin) StreamChunk(ctx *RequestContext, chunk []byte) ([]byte, error) { return chunk, nil }
func (m *multiPlugin) PreLLM(ctx *RequestContext) (*RequestContext, error)       { return ctx, nil }
func (m *multiPlugin) PostLLM(ctx *RequestContext, resp *Response) (*Response, error) { return resp, nil }
func (m *multiPlugin) OnTrace(trace *RequestTrace)                              {}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestManagerRegisterMultipleInterfaces(t *testing.T) {
	m := NewManager(testLogger())
	mp := &multiPlugin{stubPlugin: stubPlugin{name: "multi"}}
	m.Register(mp)

	if len(m.TransportPlugins()) != 1 {
		t.Errorf("expected 1 transport plugin, got %d", len(m.TransportPlugins()))
	}
	if len(m.LLMPlugins()) != 1 {
		t.Errorf("expected 1 llm plugin, got %d", len(m.LLMPlugins()))
	}
	if len(m.ObservabilityPlugins()) != 1 {
		t.Errorf("expected 1 observability plugin, got %d", len(m.ObservabilityPlugins()))
	}
}

func TestManagerRegisterBaseOnly(t *testing.T) {
	m := NewManager(testLogger())
	m.Register(&stubPlugin{name: "base"})

	if len(m.TransportPlugins()) != 0 {
		t.Errorf("expected 0 transport plugins, got %d", len(m.TransportPlugins()))
	}
	if len(m.LLMPlugins()) != 0 {
		t.Errorf("expected 0 llm plugins, got %d", len(m.LLMPlugins()))
	}
}

func TestManagerInitAll(t *testing.T) {
	m := NewManager(testLogger())
	p1 := &stubPlugin{name: "a"}
	p2 := &stubPlugin{name: "b"}
	m.Register(p1)
	m.Register(p2)

	configs := map[string]map[string]any{
		"a": {"key": "val-a"},
		"b": {"key": "val-b"},
	}
	if err := m.InitAll(configs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1.initCfg["key"] != "val-a" {
		t.Errorf("expected val-a, got %v", p1.initCfg["key"])
	}
	if p2.initCfg["key"] != "val-b" {
		t.Errorf("expected val-b, got %v", p2.initCfg["key"])
	}
}

func TestManagerInitAllNilConfig(t *testing.T) {
	m := NewManager(testLogger())
	p := &stubPlugin{name: "x"}
	m.Register(p)

	if err := m.InitAll(nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Init is called with nil config.
	if p.initCfg != nil {
		t.Errorf("expected nil config, got %v", p.initCfg)
	}
}

func TestManagerInitAllError(t *testing.T) {
	m := NewManager(testLogger())
	m.Register(&stubPlugin{name: "good"})
	m.Register(&stubPlugin{name: "bad", initErr: fmt.Errorf("init boom")})
	m.Register(&stubPlugin{name: "never"})

	err := m.InitAll(nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != `plugin "bad" init failed: init boom` {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestManagerCloseAllReverseOrder(t *testing.T) {
	m := NewManager(testLogger())
	order := &[]string{}
	m.Register(&stubPlugin{name: "first", closeOrder: order})
	m.Register(&stubPlugin{name: "second", closeOrder: order})
	m.Register(&stubPlugin{name: "third", closeOrder: order})

	m.CloseAll()

	expected := []string{"third", "second", "first"}
	if len(*order) != len(expected) {
		t.Fatalf("expected %d closes, got %d", len(expected), len(*order))
	}
	for i, name := range expected {
		if (*order)[i] != name {
			t.Errorf("position %d: expected %s, got %s", i, name, (*order)[i])
		}
	}
}

func TestManagerCloseAllLogsErrors(t *testing.T) {
	m := NewManager(testLogger())
	p := &stubPlugin{name: "err-close", closeErr: fmt.Errorf("close boom")}
	m.Register(p)

	// Should not panic.
	m.CloseAll()
	if !p.closed {
		t.Error("expected plugin to be closed")
	}
}
