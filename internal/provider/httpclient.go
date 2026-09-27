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
// timeout bounds the wait for response headers and each gap between body
// reads, not the total: Client.Timeout would sever streams longer than timeout.
func NewHTTPClient(timeout time.Duration) *http.Client {
	base := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          0,
		MaxIdleConnsPerHost:   1024,
		IdleConnTimeout:       90 * time.Second,
	}
	if timeout <= 0 {
		return &http.Client{Transport: base}
	}
	return &http.Client{Transport: &idleTimeoutTransport{Transport: base, timeout: timeout}}
}

// errBodyIdle is returned by body reads after the upstream sent nothing for
// the idle timeout.
var errBodyIdle error = bodyIdleError{}

type bodyIdleError struct{}

func (bodyIdleError) Error() string   { return "upstream response body idle timeout" }
func (bodyIdleError) Timeout() bool   { return true }
func (bodyIdleError) Temporary() bool { return true }

// idleTimeoutTransport cancels a request whose response body goes silent for
// timeout. Embedding keeps CloseIdleConnections reachable from http.Client.
type idleTimeoutTransport struct {
	*http.Transport
	timeout time.Duration
}

func (t *idleTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := t.Transport.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	b := &idleTimeoutBody{ReadCloser: resp.Body, timeout: t.timeout, cancel: cancel}
	b.timer = time.AfterFunc(t.timeout, func() {
		b.expired.Store(true)
		cancel(errBodyIdle)
	})
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
		err = errBodyIdle
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
