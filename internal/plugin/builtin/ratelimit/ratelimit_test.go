package ratelimit

import (
	"fmt"
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
		trusted  []any
		remote   string
		xff      string
		xri      string
		// xffLines / xriLines set repeated header lines, as proxies that add
		// rather than append produce. Mutually exclusive with xff / xri.
		xffLines []string
		xriLines []string
		expected string
	}{
		// No trusted proxies configured: headers are ignored outright.
		{
			name:     "no trusted proxies ignores XFF",
			remote:   "5.6.7.8:1234",
			xff:      "1.2.3.4",
			expected: "5.6.7.8",
		},
		{
			name:     "no trusted proxies ignores X-Real-IP",
			remote:   "5.6.7.8:1234",
			xri:      "2.3.4.5",
			expected: "5.6.7.8",
		},
		{
			name:     "RemoteAddr only",
			remote:   "5.6.7.8:1234",
			expected: "5.6.7.8",
		},
		{
			name:     "RemoteAddr without port",
			remote:   "5.6.7.8",
			expected: "5.6.7.8",
		},
		{
			name:     "unparseable RemoteAddr collapses to a single bucket",
			remote:   "not-an-address",
			xff:      "1.2.3.4",
			expected: unknownIP,
		},

		// Spoofing from an untrusted peer must not move the key.
		{
			name:     "spoofed XFF from untrusted peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "203.0.113.9:4444",
			xff:      "1.2.3.4",
			expected: "203.0.113.9",
		},
		{
			name:     "spoofed chain from untrusted peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "203.0.113.9:4444",
			xff:      "1.2.3.4, 10.0.0.1, 10.0.0.2",
			expected: "203.0.113.9",
		},
		{
			name:     "spoofed X-Real-IP from untrusted peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "203.0.113.9:4444",
			xri:      "1.2.3.4",
			expected: "203.0.113.9",
		},

		// Trusted peer: honour the headers.
		{
			name:     "trusted peer single hop",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4",
			expected: "1.2.3.4",
		},
		{
			name:     "trusted peer multi-hop takes rightmost untrusted",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4, 198.51.100.7, 10.0.0.2",
			expected: "198.51.100.7",
		},
		{
			name:     "trusted peer chain of only trusted hops falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "10.0.0.3, 10.0.0.2",
			expected: "10.0.0.1",
		},
		{
			name:     "client-injected leftmost entry is not used",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "9.9.9.9, 203.0.113.5, 10.0.0.2",
			expected: "203.0.113.5",
		},
		{
			name:     "trusted peer X-Real-IP when no XFF",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xri:      "2.3.4.5",
			expected: "2.3.4.5",
		},
		{
			name:     "XFF wins over X-Real-IP",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4",
			xri:      "2.3.4.5",
			expected: "1.2.3.4",
		},
		{
			name:     "X-Real-IP naming a trusted proxy falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xri:      "10.0.0.7",
			expected: "10.0.0.1",
		},
		{
			name:     "bare IP entry in trusted_proxies",
			trusted:  []any{"10.0.0.1"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4",
			expected: "1.2.3.4",
		},
		{
			name:     "peer outside a bare trusted IP is not trusted",
			trusted:  []any{"10.0.0.1"},
			remote:   "10.0.0.2:1234",
			xff:      "1.2.3.4",
			expected: "10.0.0.2",
		},
		{
			name:     "XFF entry carrying a port",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4:5678",
			expected: "1.2.3.4",
		},
		{
			name:     "whitespace around chain entries",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "  1.2.3.4 ,  10.0.0.2  ",
			expected: "1.2.3.4",
		},

		// Malformed values must never mint a bucket of their own.
		{
			name:     "malformed XFF falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "not-an-ip",
			expected: "10.0.0.1",
		},
		{
			name:     "malformed rightmost hop aborts the walk",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4, garbage",
			expected: "10.0.0.1",
		},
		{
			name:     "malformed leftmost hop is never reached",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "garbage, 198.51.100.7",
			expected: "198.51.100.7",
		},
		{
			name:     "empty XFF entry falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "1.2.3.4,",
			expected: "10.0.0.1",
		},
		{
			name:     "malformed X-Real-IP falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xri:      "999.999.999.999",
			expected: "10.0.0.1",
		},
		{
			name:     "XFF header of only commas falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      ",,,",
			expected: "10.0.0.1",
		},

		// Repeated header lines: proxies that add a line instead of appending
		// leave the client's own line first, so only the last line was written
		// by the trusted peer.
		{
			name:     "multi-line XFF walks the last line",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:999",
			xffLines: []string{"6.6.6.6", "203.0.113.9"},
			expected: "203.0.113.9",
		},
		{
			name:     "multi-line XFF with a chain on the proxy line",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:999",
			xffLines: []string{"6.6.6.6", "203.0.113.9, 10.0.0.2"},
			expected: "203.0.113.9",
		},
		{
			name:     "multi-line XFF from an untrusted peer is still ignored",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "203.0.113.9:4444",
			xffLines: []string{"6.6.6.6", "1.2.3.4"},
			expected: "203.0.113.9",
		},
		{
			name:     "multi-line XFF with a malformed last line falls back to peer",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:999",
			xffLines: []string{"6.6.6.6", "garbage"},
			expected: "10.0.0.1",
		},
		{
			name:     "multi-line X-Real-IP takes the last line",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:999",
			xriLines: []string{"6.6.6.6", "203.0.113.9"},
			expected: "203.0.113.9",
		},

		// IPv6.
		{
			name:     "IPv6 peer untrusted ignores XFF",
			trusted:  []any{"2001:db8::/32"},
			remote:   "[2001:db9::1]:4444",
			xff:      "2001:db8::99",
			expected: "2001:db9::1",
		},
		{
			name:     "IPv6 trusted peer honours IPv6 XFF",
			trusted:  []any{"2001:db8::/32"},
			remote:   "[2001:db8::1]:4444",
			xff:      "2606:4700::1111, 2001:db8::2",
			expected: "2606:4700::1111",
		},
		{
			name:     "IPv6 trusted peer with bracketed XFF entry",
			trusted:  []any{"2001:db8::/32"},
			remote:   "[2001:db8::1]:4444",
			xff:      "[2606:4700::1111]:443",
			expected: "2606:4700::1111",
		},
		{
			name:     "IPv6 trusted peer with IPv4 client",
			trusted:  []any{"2001:db8::/32"},
			remote:   "[2001:db8::1]:4444",
			xff:      "1.2.3.4",
			expected: "1.2.3.4",
		},
		{
			name:     "IPv4-mapped IPv6 peer matches an IPv4 CIDR",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "[::ffff:10.0.0.1]:1234",
			xff:      "1.2.3.4",
			expected: "1.2.3.4",
		},
		{
			name:     "IPv4-mapped XFF entry normalises to IPv4",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "::ffff:1.2.3.4",
			expected: "1.2.3.4",
		},
		{
			name:     "IPv6 zone is stripped from the key",
			trusted:  []any{"10.0.0.0/8"},
			remote:   "10.0.0.1:1234",
			xff:      "fe80::1%eth0",
			expected: "fe80::1",
		},
		{
			name:     "mixed IPv4 and IPv6 trusted set",
			trusted:  []any{"10.0.0.0/8", "2001:db8::/32"},
			remote:   "[2001:db8::1]:4444",
			xff:      "203.0.113.5, 10.0.0.2, 2001:db8::2",
			expected: "203.0.113.5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New()
			cfg := map[string]any{}
			if tt.trusted != nil {
				cfg["trusted_proxies"] = tt.trusted
			}
			if err := p.Init(cfg); err != nil {
				t.Fatalf("Init failed: %v", err)
			}

			req, _ := http.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			for _, line := range tt.xffLines {
				req.Header.Add("X-Forwarded-For", line)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}
			for _, line := range tt.xriLines {
				req.Header.Add("X-Real-IP", line)
			}
			got := p.clientIP(req)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestInitTrustedProxiesInvalid(t *testing.T) {
	tests := []struct {
		name string
		cfg  any
	}{
		{"bad CIDR", []any{"10.0.0.0/99"}},
		{"bad address", []any{"not-an-ip"}},
		{"non-string entry", []any{42}},
		{"not a list", "10.0.0.0/8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New()
			if err := p.Init(map[string]any{"trusted_proxies": tt.cfg}); err == nil {
				t.Fatal("expected Init to reject invalid trusted_proxies")
			}
		})
	}
}

func TestInitTrustedProxiesAcceptsStringSlice(t *testing.T) {
	p := New()
	if err := p.Init(map[string]any{"trusted_proxies": []string{"10.0.0.0/8", " ", "192.168.1.1"}}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if len(p.trustedProxies) != 2 {
		t.Fatalf("expected 2 prefixes, got %d", len(p.trustedProxies))
	}
}

// A client varying X-Forwarded-For from an untrusted peer must not mint a new
// bucket per request; all of them share the peer's bucket.
func TestSpoofedXFFCannotEscapePerIPLimit(t *testing.T) {
	p := New()
	if err := p.Init(map[string]any{
		"requests_per_minute": 2,
		"per_ip":              true,
		"trusted_proxies":     []any{"10.0.0.0/8"},
	}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = p.Close() }()

	allowed := 0
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
		req.RemoteAddr = "203.0.113.9:4444"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("1.2.3.%d", i))
		ctx := &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
		_ = p.PreHTTP(ctx)
		if !ctx.ShortCircuit {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("expected 2 allowed requests, got %d", allowed)
	}

	p.mu.Lock()
	n := len(p.buckets)
	p.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 bucket for the spoofing client, got %d", n)
	}
}

// Distinct clients behind a trusted proxy still get independent buckets.
func TestTrustedProxyGivesPerClientBuckets(t *testing.T) {
	p := New()
	if err := p.Init(map[string]any{
		"requests_per_minute": 1,
		"per_ip":              true,
		"trusted_proxies":     []any{"10.0.0.0/8"},
	}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer func() { _ = p.Close() }()

	newReq := func(xff string) *plugin.RequestContext {
		req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", xff)
		return &plugin.RequestContext{Request: req, Metadata: make(map[string]any)}
	}

	ctxA := newReq("1.2.3.4, 10.0.0.2")
	_ = p.PreHTTP(ctxA)
	if ctxA.ShortCircuit {
		t.Fatal("client A first request should have been allowed")
	}

	ctxA2 := newReq("1.2.3.4, 10.0.0.2")
	_ = p.PreHTTP(ctxA2)
	if !ctxA2.ShortCircuit {
		t.Fatal("client A second request should have been rate-limited")
	}

	ctxB := newReq("5.6.7.8, 10.0.0.2")
	_ = p.PreHTTP(ctxB)
	if ctxB.ShortCircuit {
		t.Fatal("client B should NOT be rate-limited")
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
