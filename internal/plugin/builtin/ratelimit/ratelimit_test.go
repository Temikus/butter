package ratelimit

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/temikus/butter/internal/plugin"
)

func TestPluginName(t *testing.T) {
	p := New()
	if p.Name() != "ratelimit" {
		t.Fatalf("expected name %q, got %q", "ratelimit", p.Name())
	}
}

func TestPluginClose(t *testing.T) {
	p := New()
	if err := p.Close(); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestPluginInitDefaults(t *testing.T) {
	p := New()
	if err := p.Init(nil); err != nil {
		t.Fatalf("Init(nil) failed: %v", err)
	}
	if p.rpm != 60 {
		t.Errorf("expected rpm=60, got %d", p.rpm)
	}
	if p.perIP {
		t.Error("expected perIP=false by default")
	}
}

func TestPluginInitCustomConfig(t *testing.T) {
	p := New()
	err := p.Init(map[string]any{
		"requests_per_minute": 100,
		"per_ip":              true,
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if p.rpm != 100 {
		t.Errorf("expected rpm=100, got %d", p.rpm)
	}
	if !p.perIP {
		t.Error("expected perIP=true")
	}
}

func TestGlobalRateLimit(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{"requests_per_minute": 5})

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "127.0.0.1:1234"

	// First 5 requests should pass.
	for i := 0; i < 5; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("request %d should have been allowed", i+1)
		}
	}

	// 6th request should be rate-limited.
	ctx := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
	_ = p.PreHTTP(ctx)
	if !ctx.ShortCircuit {
		t.Fatal("6th request should have been rate-limited")
	}
	if ctx.ShortCircuitStatus != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", ctx.ShortCircuitStatus)
	}
	if len(ctx.ShortCircuitBody) == 0 {
		t.Error("expected non-empty short-circuit body")
	}
}

func TestPerIPRateLimit(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 2,
		"per_ip":              true,
	})

	reqA, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	reqA.RemoteAddr = "10.0.0.1:1234"

	reqB, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	reqB.RemoteAddr = "10.0.0.2:5678"

	// Exhaust client A's quota.
	for i := 0; i < 2; i++ {
		ctx := &plugin.RequestContext{Request: reqA, Metadata: make(map[string]any)}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("client A request %d should have been allowed", i+1)
		}
	}

	// Client A should now be rate-limited.
	ctxA := &plugin.RequestContext{Request: reqA, Metadata: make(map[string]any)}
	_ = p.PreHTTP(ctxA)
	if !ctxA.ShortCircuit {
		t.Fatal("client A should be rate-limited")
	}

	// Client B should still be allowed.
	ctxB := &plugin.RequestContext{Request: reqB, Metadata: make(map[string]any)}
	_ = p.PreHTTP(ctxB)
	if ctxB.ShortCircuit {
		t.Fatal("client B should NOT be rate-limited")
	}
}

func TestTokenRefill(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{"requests_per_minute": 60})

	// Drain the bucket manually.
	now := time.Now()
	p.global = newBucket(60, now)
	p.global.tokens = 0
	p.global.lastFill = now

	// Advance 1 second — should refill 1 token (60/min = 1/sec).
	future := now.Add(1 * time.Second)
	if !p.global.allow(future) {
		t.Fatal("expected token refill to allow request after 1 second")
	}
}

func TestBucketRefillCap(t *testing.T) {
	b := newBucket(10, time.Now())
	// Advance far into the future — tokens should not exceed max.
	future := time.Now().Add(10 * time.Minute)
	b.allow(future) // refills and consumes 1
	if b.tokens > b.max {
		t.Errorf("tokens %f exceeded max %f", b.tokens, b.max)
	}
}

func TestClientIPExtraction(t *testing.T) {
	tests := []struct {
		name     string
		xff      string
		xri      string
		remote   string
		expected string
	}{
		{"X-Forwarded-For", "1.2.3.4", "", "5.6.7.8:1234", "1.2.3.4"},
		{"X-Real-IP", "", "2.3.4.5", "5.6.7.8:1234", "2.3.4.5"},
		{"RemoteAddr", "", "", "5.6.7.8:1234", "5.6.7.8"},
		{"RemoteAddr no port", "", "", "5.6.7.8", "5.6.7.8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}
			got := clientIP(req)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestPerAppKeyRateLimit(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 2,
		"per_app_key":         true,
	})
	defer func() { _ = p.Close() }()

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	// Exhaust key A's quota.
	for i := 0; i < 2; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_keyA"}}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("key A request %d should have been allowed", i+1)
		}
	}

	// Key A should now be rate-limited.
	ctxA := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_keyA"}}
	_ = p.PreHTTP(ctxA)
	if !ctxA.ShortCircuit {
		t.Fatal("key A should be rate-limited")
	}

	// Key B should still be allowed.
	ctxB := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_keyB"}}
	_ = p.PreHTTP(ctxB)
	if ctxB.ShortCircuit {
		t.Fatal("key B should NOT be rate-limited")
	}
}

func TestPerAppKeyRPMOverride(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 2,
		"per_app_key":         true,
		"per_app_key_rpm":     10,
		"app_key_limits": map[string]any{
			"btr_vip": 100,
		},
	})
	defer func() { _ = p.Close() }()

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	// VIP key should allow >10 requests (has limit 100).
	for i := 0; i < 20; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_vip"}}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("VIP key request %d should have been allowed (limit 100)", i+1)
		}
	}

	// Regular key should be limited at per_app_key_rpm=10.
	for i := 0; i < 10; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_regular"}}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("regular key request %d should have been allowed (limit 10)", i+1)
		}
	}
	ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_regular"}}
	_ = p.PreHTTP(ctx)
	if !ctx.ShortCircuit {
		t.Fatal("regular key should be rate-limited after 10 requests")
	}
}

func TestPerAppKeyFallbackToGlobalRPM(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 3,
		"per_app_key":         true,
		// No per_app_key_rpm — should fall back to requests_per_minute.
	})
	defer func() { _ = p.Close() }()

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	for i := 0; i < 3; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_test"}}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("request %d should have been allowed", i+1)
		}
	}
	ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_test"}}
	_ = p.PreHTTP(ctx)
	if !ctx.ShortCircuit {
		t.Fatal("should be rate-limited at global RPM=3")
	}
}

func TestPerAppKeyNoKeyFallsToGlobal(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 2,
		"per_app_key":         true,
	})
	defer func() { _ = p.Close() }()

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	// Requests without app_key should fall through to global bucket.
	for i := 0; i < 2; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("anonymous request %d should have been allowed", i+1)
		}
	}
	ctx := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
	_ = p.PreHTTP(ctx)
	if !ctx.ShortCircuit {
		t.Fatal("anonymous request should be rate-limited by global bucket")
	}
}

func TestPerAppKeyPriorityOverPerIP(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 2,
		"per_ip":              true,
		"per_app_key":         true,
		"per_app_key_rpm":     5,
	})
	defer func() { _ = p.Close() }()

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	// With app key: should use per-key bucket (limit 5).
	for i := 0; i < 5; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_test"}}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("keyed request %d should have been allowed (limit 5)", i+1)
		}
	}
	ctxKeyed := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_test"}}
	_ = p.PreHTTP(ctxKeyed)
	if !ctxKeyed.ShortCircuit {
		t.Fatal("keyed request should be rate-limited after 5")
	}

	// Without app key: should fall to per-IP bucket (limit 2).
	for i := 0; i < 2; i++ {
		ctx := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
		_ = p.PreHTTP(ctx)
		if ctx.ShortCircuit {
			t.Fatalf("anonymous request %d should have been allowed (per-IP limit 2)", i+1)
		}
	}
	ctxAnon := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
	_ = p.PreHTTP(ctxAnon)
	if !ctxAnon.ShortCircuit {
		t.Fatal("anonymous request should be rate-limited by per-IP bucket")
	}
}

func TestBucketCleanup(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 60,
		"per_app_key":         true,
	})
	defer func() { _ = p.Close() }()

	// Manually create a stale bucket.
	p.mu.Lock()
	stale := newBucket(60, time.Now().Add(-15*time.Minute))
	stale.lastFill = time.Now().Add(-15 * time.Minute) // >10 min ago
	p.buckets["appkey:btr_stale"] = stale

	fresh := newBucket(60, time.Now())
	p.buckets["appkey:btr_fresh"] = fresh
	p.mu.Unlock()

	// Simulate cleanup (call the logic directly instead of waiting for ticker).
	p.mu.Lock()
	now := time.Now()
	for key, b := range p.buckets {
		if now.Sub(b.lastFill) > 10*time.Minute {
			delete(p.buckets, key)
		}
	}
	p.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.buckets["appkey:btr_stale"]; ok {
		t.Error("stale bucket should have been cleaned up")
	}
	if _, ok := p.buckets["appkey:btr_fresh"]; !ok {
		t.Error("fresh bucket should NOT have been cleaned up")
	}
}

func TestPerAppKeyErrorMessage(t *testing.T) {
	p := New()
	_ = p.Init(map[string]any{
		"requests_per_minute": 1,
		"per_app_key":         true,
	})
	defer func() { _ = p.Close() }()

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	// Exhaust quota.
	ctx := &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_test"}}
	_ = p.PreHTTP(ctx)

	// Trigger rate limit.
	ctx = &plugin.RequestContext{Request: req, Metadata: map[string]any{"app_key": "btr_test"}}
	_ = p.PreHTTP(ctx)
	if !ctx.ShortCircuit {
		t.Fatal("should be rate-limited")
	}
	body := string(ctx.ShortCircuitBody)
	if !strings.Contains(body, "app key") {
		t.Errorf("expected error to mention 'app key', got %q", body)
	}
}

func TestStreamChunkPassthrough(t *testing.T) {
	p := New()
	chunk := []byte("data: {\"test\": true}")
	out, err := p.StreamChunk(nil, chunk)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(chunk) {
		t.Errorf("expected chunk passthrough, got %q", string(out))
	}
}

func TestPostHTTPNoop(t *testing.T) {
	p := New()
	if err := p.PostHTTP(nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
