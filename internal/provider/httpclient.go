package provider

import (
	"net"
	"net/http"
	"time"
)

// NewHTTPClient returns the shared upstream client used by all HTTP providers.
//
// The per-host idle cap must cover peak concurrency to one provider: surplus
// connections are closed on return and redialed (TCP+TLS) by the next burst.
// Total idle is left unbounded since the set of provider hosts is small.
//
// timeout bounds the wait for response headers, not the body: Client.Timeout
// would sever streams longer than timeout. Body reads end via request context.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          0,
			MaxIdleConnsPerHost:   1024,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}
