package provider

import (
	"net/http"
	"time"
)

// NewHTTPClient returns the shared upstream client used by all HTTP providers.
//
// The per-host idle cap must cover peak concurrency to one provider: surplus
// connections are closed on return and redialed (TCP+TLS) by the next burst.
// Total idle is left unbounded since the set of provider hosts is small.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 1024,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: timeout,
	}
}
