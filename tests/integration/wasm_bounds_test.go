//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/temikus/butter/internal/plugin"
	pluginwasm "github.com/temikus/butter/internal/plugin/wasm"
	"github.com/temikus/butter/internal/plugin/wasm/wasmtest"
)

// hookObserver wraps a TransportPlugin and reports the outcome of each
// PreHTTP call, so a test can observe a hook the chain runs and swallows.
type hookObserver struct {
	plugin.TransportPlugin
	done chan error
}

func (h *hookObserver) PreHTTP(ctx *plugin.RequestContext) error {
	err := h.TransportPlugin.PreHTTP(ctx)
	select {
	case h.done <- err:
	default:
	}
	return err
}

// TestWASMHookAbortsOnClientDisconnect asserts that a client hanging up
// unwinds a WASM hook that would otherwise spin for the whole timeout.
func TestWASMHookAbortsOnClientDisconnect(t *testing.T) {
	wasmPath := wasmtest.Build(t, "spin")

	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	// The timeout is far longer than the test waits: if the hook ends in
	// time, only the client disconnect can have ended it.
	wp := pluginwasm.New("spin", wasmPath, logger, pluginwasm.WithTimeout(60*time.Second))
	if err := wp.Init(nil); err != nil {
		t.Fatalf("wasm plugin Init() error: %v", err)
	}
	t.Cleanup(func() { _ = wp.Close() })

	observer := &hookObserver{TransportPlugin: wp, done: make(chan error, 1)}

	mock := mockOpenAI(t, nil)
	ts := newServerCfg().
		withProvider("openai", mock.URL).
		withDefault("openai").
		withPlugin(observer).
		build(t)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	clientDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		clientDone <- err
	}()

	// Give the hook time to enter its loop, then hang up.
	time.Sleep(200 * time.Millisecond)
	cancel()

	if err := <-clientDone; err == nil {
		t.Fatal("client request should have failed after cancellation")
	}

	select {
	case err := <-observer.done:
		if !errors.Is(err, pluginwasm.ErrHookCanceled) {
			t.Errorf("hook error = %v, want ErrHookCanceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WASM hook still running 10s after the client disconnected")
	}

	// The aborted call must release its instance.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if wp.ActiveInstances() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("plugin still holds %d instance(s) after the aborted request", wp.ActiveInstances())
}

// discardWriter drops log output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
