package ratelimit

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/temikus/butter/internal/plugin"
)

// unknownIP keys requests whose peer address cannot be parsed. Bounded by
// construction: RemoteAddr comes from the socket, not from the client.
const unknownIP = "unknown"

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

	// trustedProxies gates forwarding headers. Empty = never trust them.
	trustedProxies []netip.Prefix
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
	p.initAppKeyConfig(cfg)
	if v, ok := cfg["trusted_proxies"]; ok {
		prefixes, err := parseTrustedProxies(v)
		if err != nil {
			return err
		}
		p.trustedProxies = prefixes
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

// initAppKeyConfig applies the per-app-key portion of the plugin config.
func (p *Plugin) initAppKeyConfig(cfg map[string]any) {
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
		key := "ip:" + p.clientIP(ctx.Request)
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

// parseTrustedProxies accepts a YAML list of CIDRs or bare IPs. A bare IP
// becomes a single-host prefix. Any unparseable entry is a config error.
func parseTrustedProxies(v any) ([]netip.Prefix, error) {
	var raw []string
	switch vv := v.(type) {
	case []any:
		for _, e := range vv {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("ratelimit: trusted_proxies entry %v is not a string", e)
			}
			raw = append(raw, s)
		}
	case []string:
		raw = vv
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("ratelimit: trusted_proxies must be a list, got %T", v)
	}

	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			pfx, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("ratelimit: invalid trusted_proxies CIDR %q: %w", entry, err)
			}
			// Masked form so Contains isn't skewed by host bits.
			prefixes = append(prefixes, pfx.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("ratelimit: invalid trusted_proxies address %q: %w", entry, err)
		}
		addr = normalizeAddr(addr)
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// normalizeAddr strips the zone and unmaps 4-in-6 forms so that
// ::ffff:10.0.0.1 and 10.0.0.1 compare and key identically.
func normalizeAddr(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

// parseAddr parses a host that may carry a port and/or brackets.
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return normalizeAddr(addr), true
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			return normalizeAddr(addr), true
		}
	}
	// Bracketed IPv6 without a port, e.g. "[::1]".
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if addr, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return normalizeAddr(addr), true
		}
	}
	return netip.Addr{}, false
}

// isTrusted reports whether addr falls inside the configured proxy set.
func (p *Plugin) isTrusted(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, pfx := range p.trustedProxies {
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

// clientIP resolves the rate-limit key for a request.
//
// Forwarding headers are honoured only when the peer address is inside
// trusted_proxies; otherwise the peer address alone is used, so a client can't
// mint unbounded buckets by varying X-Forwarded-For. Within a trusted chain the
// key is the rightmost entry that is not itself a trusted proxy (the last hop
// our infrastructure actually observed). A malformed entry aborts the walk and
// falls back to the peer address rather than trusting anything further left.
func (p *Plugin) clientIP(r *http.Request) string {
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return unknownIP
	}
	if !p.isTrusted(peer) {
		return peer.String()
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			addr, ok := parseAddr(hops[i])
			if !ok {
				break
			}
			if !p.isTrusted(addr) {
				return addr.String()
			}
		}
	}

	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		if addr, ok := parseAddr(xri); ok && !p.isTrusted(addr) {
			return addr.String()
		}
	}

	return peer.String()
}
