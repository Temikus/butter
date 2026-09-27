package provider

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// NewHTTPClient returns the shared upstream client used by all HTTP providers.
//
// The per-host idle cap must cover peak concurrency to one provider: surplus
// connections are closed on return and redialed (TCP+TLS) by the next burst.
// Total idle is left unbounded since the set of provider hosts is small.
//
// timeout bounds the upload plus header wait, then each gap between body
// reads, not the total: Client.Timeout would sever streams longer than timeout.
// (ResponseHeaderTimeout alone would not: it starts only after the upload.)
func NewHTTPClient(timeout time.Duration) *http.Client {
	base := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        0,
		MaxIdleConnsPerHost: 1024,
		IdleConnTimeout:     90 * time.Second,
	}
	if timeout <= 0 {
		return &http.Client{Transport: base}
	}
	return &http.Client{Transport: &idleTimeoutTransport{Transport: base, timeout: timeout}}
}

// errUpstreamIdle is returned once the upstream made no progress for timeout.
var errUpstreamIdle error = upstreamIdleError{}

type upstreamIdleError struct{}

func (upstreamIdleError) Error() string   { return "upstream idle timeout" }
func (upstreamIdleError) Timeout() bool   { return true }
func (upstreamIdleError) Temporary() bool { return true }

// idleTimeoutTransport cancels a request whose upstream goes silent for
// timeout. Embedding keeps CloseIdleConnections reachable from http.Client.
type idleTimeoutTransport struct {
	*http.Transport
	timeout time.Duration
}

func (t *idleTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	b := &idleTimeoutBody{timeout: t.timeout, cancel: cancel}
	b.timer = time.AfterFunc(t.timeout, func() {
		b.expired.Store(true)
		cancel(errUpstreamIdle)
	})
	resp, err := t.Transport.RoundTrip(req.WithContext(ctx))
	if err != nil {
		b.timer.Stop()
		cancel(nil)
		if b.expired.Load() {
			return nil, errUpstreamIdle
		}
		return nil, err
	}
	b.timer.Reset(t.timeout)
	b.ReadCloser = resp.Body
	resp.Body = b
	return resp, nil
}

type idleTimeoutBody struct {
	io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	expired atomic.Bool
	cancel  context.CancelCauseFunc
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.timeout)
	}
	if err != nil && b.expired.Load() {
		err = errUpstreamIdle
	}
	return n, err
}

// Close cancels only after the inner Close so a fully read connection is
// already back in the idle pool.
func (b *idleTimeoutBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel(nil)
	return err
}
