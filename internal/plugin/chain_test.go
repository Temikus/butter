package plugin

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// trackingTransportPlugin records call order and can optionally error.
type trackingTransportPlugin struct {
	stubPlugin
	preOrder     *[]string
	postOrder    *[]string
	chunkOrder   *[]string
	preErr       error
	postErr      error
	chunkErr     error
	chunkTransform func([]byte) []byte
}

func (p *trackingTransportPlugin) PreHTTP(ctx *RequestContext) error {
	if p.preOrder != nil {
		*p.preOrder = append(*p.preOrder, p.name)
	}
	return p.preErr
}

func (p *trackingTransportPlugin) PostHTTP(ctx *RequestContext) error {
	if p.postOrder != nil {
		*p.postOrder = append(*p.postOrder, p.name)
	}
	return p.postErr
}

func (p *trackingTransportPlugin) StreamChunk(ctx *RequestContext, chunk []byte) ([]byte, error) {
	if p.chunkOrder != nil {
		*p.chunkOrder = append(*p.chunkOrder, p.name)
	}
	if p.chunkErr != nil {
		return nil, p.chunkErr
	}
	if p.chunkTransform != nil {
		return p.chunkTransform(chunk), nil
	}
	return chunk, nil
}

// trackingLLMPlugin records call order and can modify context/response.
type trackingLLMPlugin struct {
	stubPlugin
	preOrder    *[]string
	postOrder   *[]string
	preErr      error
	postErr     error
	preTransform  func(*RequestContext) *RequestContext
	postTransform func(*Response) *Response
}

func (p *trackingLLMPlugin) PreLLM(ctx *RequestContext) (*RequestContext, error) {
	if p.preOrder != nil {
		*p.preOrder = append(*p.preOrder, p.name)
	}
	if p.preErr != nil {
		return nil, p.preErr
	}
	if p.preTransform != nil {
		return p.preTransform(ctx), nil
	}
	return ctx, nil
}

func (p *trackingLLMPlugin) PostLLM(ctx *RequestContext, resp *Response) (*Response, error) {
	if p.postOrder != nil {
		*p.postOrder = append(*p.postOrder, p.name)
	}
	if p.postErr != nil {
		return nil, p.postErr
	}
	if p.postTransform != nil {
		return p.postTransform(resp), nil
	}
	return resp, nil
}

// trackingObsPlugin records traces it receives.
type trackingObsPlugin struct {
	stubPlugin
	mu     sync.Mutex
	traces []*RequestTrace
	wg     sync.WaitGroup
}

func (p *trackingObsPlugin) OnTrace(trace *RequestTrace) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.traces = append(p.traces, trace)
	p.wg.Done()
}

func newTestChain() (*Manager, *Chain) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := NewManager(logger)
	c := NewChain(m, logger)
	return m, c
}

func TestChainPreHTTPOrder(t *testing.T) {
	m, c := newTestChain()
	order := &[]string{}
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "a"}, preOrder: order})
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "b"}, preOrder: order})
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "c"}, preOrder: order})

	c.RunPreHTTP(&RequestContext{})

	expected := []string{"a", "b", "c"}
	assertOrder(t, *order, expected, "PreHTTP")
}

func TestChainPostHTTPReverseOrder(t *testing.T) {
	m, c := newTestChain()
	order := &[]string{}
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "a"}, postOrder: order})
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "b"}, postOrder: order})
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "c"}, postOrder: order})

	c.RunPostHTTP(&RequestContext{})

	expected := []string{"c", "b", "a"}
	assertOrder(t, *order, expected, "PostHTTP")
}

func TestChainPreLLMOrder(t *testing.T) {
	m, c := newTestChain()
	order := &[]string{}
	m.Register(&trackingLLMPlugin{stubPlugin: stubPlugin{name: "x"}, preOrder: order})
	m.Register(&trackingLLMPlugin{stubPlugin: stubPlugin{name: "y"}, preOrder: order})

	c.RunPreLLM(&RequestContext{})

	expected := []string{"x", "y"}
	assertOrder(t, *order, expected, "PreLLM")
}

func TestChainPostLLMReverseOrder(t *testing.T) {
	m, c := newTestChain()
	order := &[]string{}
	m.Register(&trackingLLMPlugin{stubPlugin: stubPlugin{name: "x"}, postOrder: order})
	m.Register(&trackingLLMPlugin{stubPlugin: stubPlugin{name: "y"}, postOrder: order})

	c.RunPostLLM(&RequestContext{}, &Response{StatusCode: 200})

	expected := []string{"y", "x"}
	assertOrder(t, *order, expected, "PostLLM")
}

func TestChainStreamChunkReverseOrder(t *testing.T) {
	m, c := newTestChain()
	order := &[]string{}
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "a"}, chunkOrder: order})
	m.Register(&trackingTransportPlugin{stubPlugin: stubPlugin{name: "b"}, chunkOrder: order})

	c.RunStreamChunk(&RequestContext{}, []byte("data"))

	expected := []string{"b", "a"}
	assertOrder(t, *order, expected, "StreamChunk")
}

func TestChainStreamChunkModification(t *testing.T) {
	m, c := newTestChain()
	m.Register(&trackingTransportPlugin{
		stubPlugin: stubPlugin{name: "upper"},
		chunkTransform: func(b []byte) []byte {
			return append([]byte("PREFIX:"), b...)
		},
	})

	result := c.RunStreamChunk(&RequestContext{}, []byte("hello"))
	if string(result) != "PREFIX:hello" {
		t.Errorf("expected PREFIX:hello, got %s", result)
	}
}

func TestChainPreLLMModifiesContext(t *testing.T) {
	m, c := newTestChain()
	m.Register(&trackingLLMPlugin{
		stubPlugin: stubPlugin{name: "rewrite"},
		preTransform: func(ctx *RequestContext) *RequestContext {
			ctx.Model = "rewritten-model"
			ctx.Body = []byte(`{"rewritten":true}`)
			return ctx
		},
	})

	ctx := &RequestContext{Model: "original", Body: []byte(`{}`)}
	result := c.RunPreLLM(ctx)

	if result.Model != "rewritten-model" {
		t.Errorf("expected rewritten-model, got %s", result.Model)
	}
	if string(result.Body) != `{"rewritten":true}` {
		t.Errorf("expected rewritten body, got %s", result.Body)
	}
}

func TestChainPostLLMModifiesResponse(t *testing.T) {
	m, c := newTestChain()
	m.Register(&trackingLLMPlugin{
		stubPlugin: stubPlugin{name: "inject"},
		postTransform: func(resp *Response) *Response {
			resp.Body = append(resp.Body, []byte("-modified")...)
			return resp
		},
	})

	resp := &Response{StatusCode: 200, Body: []byte("original")}
	result := c.RunPostLLM(&RequestContext{}, resp)

	if string(result.Body) != "original-modified" {
		t.Errorf("expected original-modified, got %s", result.Body)
	}
}

func TestChainFailOpenPreLLM(t *testing.T) {
	m, c := newTestChain()
	m.Register(&trackingLLMPlugin{
		stubPlugin: stubPlugin{name: "err"},
		preErr:     fmt.Errorf("boom"),
	})
	m.Register(&trackingLLMPlugin{
		stubPlugin: stubPlugin{name: "ok"},
		preTransform: func(ctx *RequestContext) *RequestContext {
			ctx.Model = "from-ok"
			return ctx
		},
	})

	ctx := &RequestContext{Model: "original"}
	result := c.RunPreLLM(ctx)

	// First plugin errors, so original ctx passes to second which modifies it.
	if result.Model != "from-ok" {
		t.Errorf("expected from-ok, got %s", result.Model)
	}
}

func TestChainFailOpenPostLLM(t *testing.T) {
	m, c := newTestChain()
	m.Register(&trackingLLMPlugin{
		stubPlugin: stubPlugin{name: "err"},
		postErr:    fmt.Errorf("boom"),
	})

	resp := &Response{StatusCode: 200, Body: []byte("original")}
	result := c.RunPostLLM(&RequestContext{}, resp)

	if string(result.Body) != "original" {
		t.Errorf("expected original response preserved, got %s", result.Body)
	}
}

func TestChainFailOpenStreamChunk(t *testing.T) {
	m, c := newTestChain()
	m.Register(&trackingTransportPlugin{
		stubPlugin: stubPlugin{name: "err"},
		chunkErr:   fmt.Errorf("chunk boom"),
	})

	result := c.RunStreamChunk(&RequestContext{}, []byte("data"))
	if string(result) != "data" {
		t.Errorf("expected original chunk preserved, got %s", result)
	}
}

func TestChainNoPlugins(t *testing.T) {
	_, c := newTestChain()

	// All methods should be no-ops.
	c.RunPreHTTP(&RequestContext{})
	c.RunPostHTTP(&RequestContext{})

	chunk := c.RunStreamChunk(&RequestContext{}, []byte("pass"))
	if string(chunk) != "pass" {
		t.Errorf("expected pass, got %s", chunk)
	}

	ctx := &RequestContext{Model: "test"}
	result := c.RunPreLLM(ctx)
	if result.Model != "test" {
		t.Errorf("expected test, got %s", result.Model)
	}

	resp := &Response{StatusCode: 200}
	rResult := c.RunPostLLM(ctx, resp)
	if rResult.StatusCode != 200 {
		t.Errorf("expected 200, got %d", rResult.StatusCode)
	}
}

func TestChainEmitTrace(t *testing.T) {
	m, c := newTestChain()
	obs := &trackingObsPlugin{stubPlugin: stubPlugin{name: "obs"}}
	obs.wg.Add(1)
	m.Register(obs)

	trace := &RequestTrace{Provider: "test", Model: "gpt-4", Duration: 100 * time.Millisecond}
	c.EmitTrace(trace)

	// Wait for async delivery.
	obs.wg.Wait()

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(obs.traces))
	}
	if obs.traces[0].Provider != "test" {
		t.Errorf("expected provider test, got %s", obs.traces[0].Provider)
	}
}

func assertOrder(t *testing.T, got, expected []string, hook string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Fatalf("%s: expected %d calls, got %d", hook, len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("%s position %d: expected %s, got %s", hook, i, expected[i], got[i])
		}
	}
}
