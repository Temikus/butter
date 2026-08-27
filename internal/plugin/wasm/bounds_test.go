package wasm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/temikus/butter/internal/plugin"
	pluginwasm "github.com/temikus/butter/internal/plugin/wasm"
	"github.com/temikus/butter/internal/plugin/wasm/wasmtest"
)

// requestContext builds a RequestContext carrying ctx, mirroring what the
// transport layer hands the plugin chain.
func requestContext(ctx context.Context) *plugin.RequestContext {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", http.NoBody).WithContext(ctx)
	return &plugin.RequestContext{
		Request:  r,
		Provider: "openai",
		Model:    "gpt-4o",
		Body:     []byte(`{"messages":[]}`),
		Metadata: map[string]any{},
	}
}

// waitForIdle polls until the plugin reports no live instances.
func waitForIdle(t *testing.T, p *pluginwasm.Plugin, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.ActiveInstances() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("plugin still holds %d instance(s) after %v", p.ActiveInstances(), within)
}

func TestHookTimeout_SpinningPlugin(t *testing.T) {
	wasmPath := wasmtest.Build(t, "spin")

	const timeout = 200 * time.Millisecond
	p := pluginwasm.New("spin", wasmPath, discardLogger, pluginwasm.WithTimeout(timeout))
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer func() { _ = p.Close() }()

	// Repeated calls: each must be bounded, typed, and must release its
	// instance before returning.
	for i := 0; i < 3; i++ {
		start := time.Now()
		err := p.PreHTTP(requestContext(context.Background()))
		elapsed := time.Since(start)

		if !errors.Is(err, pluginwasm.ErrHookTimeout) {
			t.Fatalf("call %d: PreHTTP() error = %v, want ErrHookTimeout", i, err)
		}
		// Generous upper bound: the assertion is that the call is bounded
		// at all, not that wazero unwinds within a specific slice of time.
		if elapsed > 20*timeout {
			t.Errorf("call %d: PreHTTP() took %v, want it bounded near %v", i, elapsed, timeout)
		}
		if got := p.ActiveInstances(); got != 0 {
			t.Fatalf("call %d: %d instance(s) still held after the call returned", i, got)
		}
	}
}

func TestHookCanceled_ClientDisconnect(t *testing.T) {
	wasmPath := wasmtest.Build(t, "spin")

	tests := []struct {
		name    string
		timeout time.Duration
	}{
		// Timeout far beyond the test's patience: only the request context
		// cancellation can end this call.
		{name: "timeout bounded", timeout: 60 * time.Second},
		// Opting out of the timeout must not also opt out of
		// disconnect-abort: extism only enables wazero's
		// WithCloseOnContextDone when a manifest timeout is set, so the
		// host has to set it itself.
		{name: "timeout disabled", timeout: -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := pluginwasm.New("spin", wasmPath, discardLogger, pluginwasm.WithTimeout(tc.timeout))
			if err := p.Init(nil); err != nil {
				t.Fatalf("Init() error: %v", err)
			}
			defer func() { _ = p.Close() }()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- p.PreHTTP(requestContext(ctx)) }()

			time.Sleep(100 * time.Millisecond)
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, pluginwasm.ErrHookCanceled) {
					t.Errorf("PreHTTP() error = %v, want ErrHookCanceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("PreHTTP() did not return after the request context was cancelled")
			}

			waitForIdle(t, p, 2*time.Second)
		})
	}
}

// TestMaxPagesClamp asserts an out-of-range page cap is clamped, not
// passed through to wazero, which panics above the WASM maximum.
func TestMaxPagesClamp(t *testing.T) {
	wasmPath := wasmtest.Build(t, "spin")

	p := pluginwasm.New("spin", wasmPath, discardLogger,
		pluginwasm.WithMaxPages(100_000),
		pluginwasm.WithTimeout(time.Second),
	)
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init() with an over-max page cap error: %v", err)
	}
	_ = p.Close()
}

func TestMemoryCap_AllocationPastLimit(t *testing.T) {
	wasmPath := wasmtest.Build(t, "memhog")

	// 256 pages = 16 MiB; the fixture tries to allocate 4 GiB.
	p := pluginwasm.New("memhog", wasmPath, discardLogger,
		pluginwasm.WithMaxPages(256),
		pluginwasm.WithTimeout(30*time.Second),
	)
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer func() { _ = p.Close() }()

	err := p.PreHTTP(requestContext(context.Background()))
	if err == nil {
		t.Fatal("PreHTTP() on a memory-hogging plugin should return an error")
	}
	// The cap is enforced by the guest trapping, not by a bound being hit
	// on the host side, so this is a plain hook error, not ErrHookTimeout.
	if errors.Is(err, pluginwasm.ErrHookTimeout) || errors.Is(err, pluginwasm.ErrHookCanceled) {
		t.Errorf("PreHTTP() error = %v, want a plain hook failure", err)
	}

	waitForIdle(t, p, 2*time.Second)
}
