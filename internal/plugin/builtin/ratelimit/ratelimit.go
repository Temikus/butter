package ratelimit

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/temikus/butter/internal/plugin"
)

// Plugin implements a token-bucket rate limiter as a TransportPlugin.
// It supports global, per-client-IP, and per-app-key rate limiting.
type Plugin struct {
	mu           sync.Mutex
	rpm          int
	perIP        bool
	perAppKey    bool
	appKeyRPM    int            // default RPM for app keys; 0 = fall back to rpm
	appKeyLimits map[string]int // per-key RPM overrides
	buckets      map[string]*bucket
	global       *bucket
	done         chan struct{} // signals cleanup goroutine to stop
}

type bucket struct {
	tokens    float64
	max       float64
	refillPer float64 // tokens added per nanosecond
	lastFill  time.Time
}

func newBucket(rpm int, now time.Time) *bucket {
	max := float64(rpm)
	return &bucket{
		tokens:    max,
		max:       max,
		refillPer: max / float64(time.Minute),
		lastFill:  now,
	}
}

func (b *bucket) allow(now time.Time) bool {
	elapsed := now.Sub(b.lastFill)
	b.tokens += float64(elapsed) * b.refillPer
	if b.tokens > b.max {
		b.tokens = b.max
	}
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// New creates a rate limiter plugin with sensible defaults.
func New() *Plugin {
	return &Plugin{
		rpm:     60,
		buckets: make(map[string]*bucket),
	}
}

func (p *Plugin) Name() string { return "ratelimit" }

func (p *Plugin) Init(cfg map[string]any) error {
	if cfg == nil {
		return nil
	}
	if v, ok := cfg["requests_per_minute"].(int); ok && v > 0 {
		p.rpm = v
	}
	if v, ok := cfg["per_ip"].(bool); ok {
		p.perIP = v
	}
	if v, ok := cfg["per_app_key"].(bool); ok {
		p.perAppKey = v
	}
	if v, ok := cfg["per_app_key_rpm"].(int); ok && v > 0 {
		p.appKeyRPM = v
	}
	if v, ok := cfg["app_key_limits"].(map[string]any); ok {
		p.appKeyLimits = make(map[string]int, len(v))
		for k, val := range v {
			if rpm, ok := val.(int); ok && rpm > 0 {
				p.appKeyLimits[k] = rpm
			}
		}
	}
	// Pre-create global bucket.
	p.global = newBucket(p.rpm, time.Now())
	// Start cleanup goroutine for per-key/per-IP buckets.
	if p.perAppKey || p.perIP {
		p.done = make(chan struct{})
		go p.cleanupLoop()
	}
	return nil
}

func (p *Plugin) Close() error {
	if p.done != nil {
		close(p.done)
	}
	return nil
}

// PreHTTP checks the rate limit and short-circuits with 429 if exceeded.
func (p *Plugin) PreHTTP(ctx *plugin.RequestContext) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	b, effectiveRPM := p.getBucket(ctx, now)

	if !b.allow(now) {
		ctx.ShortCircuit = true
		ctx.ShortCircuitStatus = http.StatusTooManyRequests

		msg := fmt.Sprintf("rate limit exceeded (%d requests/minute)", effectiveRPM)
		if p.perAppKey {
			if _, ok := ctx.Metadata["app_key"].(string); ok {
				msg = fmt.Sprintf("rate limit exceeded for app key (%d requests/minute)", effectiveRPM)
			}
		}
		ctx.ShortCircuitBody = []byte(fmt.Sprintf(
			`{"error":{"message":"%s","type":"rate_limit_error"}}`, msg,
		))
	}
	return nil
}

func (p *Plugin) PostHTTP(_ *plugin.RequestContext) error { return nil }

func (p *Plugin) StreamChunk(_ *plugin.RequestContext, chunk []byte) ([]byte, error) {
	return chunk, nil
}

// getBucket returns the appropriate bucket and its effective RPM limit.
// Priority: per_app_key (if enabled and key present) > per_ip > global.
func (p *Plugin) getBucket(ctx *plugin.RequestContext, now time.Time) (*bucket, int) {
	if p.perAppKey {
		if appKey, ok := ctx.Metadata["app_key"].(string); ok && appKey != "" {
			key := "appkey:" + appKey
			rpm := p.resolveAppKeyRPM(appKey)
			b, ok := p.buckets[key]
			if !ok {
				b = newBucket(rpm, now)
				p.buckets[key] = b
			}
			return b, rpm
		}
	}

	if p.perIP {
		key := "ip:" + clientIP(ctx.Request)
		b, ok := p.buckets[key]
		if !ok {
			b = newBucket(p.rpm, now)
			p.buckets[key] = b
		}
		return b, p.rpm
	}

	if p.global == nil {
		p.global = newBucket(p.rpm, now)
	}
	return p.global, p.rpm
}

// resolveAppKeyRPM returns the RPM for a given app key.
// Lookup order: app_key_limits[key] -> appKeyRPM -> rpm (global default).
func (p *Plugin) resolveAppKeyRPM(appKey string) int {
	if rpm, ok := p.appKeyLimits[appKey]; ok {
		return rpm
	}
	if p.appKeyRPM > 0 {
		return p.appKeyRPM
	}
	return p.rpm
}

// cleanupLoop periodically removes stale per-IP and per-app-key buckets.
func (p *Plugin) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.mu.Lock()
			now := time.Now()
			for key, b := range p.buckets {
				if now.Sub(b.lastFill) > 10*time.Minute {
					delete(p.buckets, key)
				}
			}
			p.mu.Unlock()
		}
	}
}

// clientIP extracts the client IP from the request, preferring
// X-Forwarded-For, then X-Real-IP, then the remote address.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
