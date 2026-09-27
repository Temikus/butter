package provider_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/temikus/butter/internal/provider"
)

// Repeated bursts of concurrent requests must reuse the connections opened by
// the first burst rather than closing surplus idle ones and redialing.
func TestNewHTTPClient_ReusesConnectionsUnderConcurrency(t *testing.T) {
	const concurrency, rounds = 64, 4

	var conns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond) // keep requests overlapping
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	client := provider.NewHTTPClient(10 * time.Second)
	defer client.CloseIdleConnections()

	for range rounds {
		var wg sync.WaitGroup
		for range concurrency {
			wg.Go(func() {
				resp, err := client.Get(srv.URL)
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			})
		}
		wg.Wait()
	}

	if got := conns.Load(); got > concurrency {
		t.Errorf("upstream saw %d connections for %d rounds of %d concurrent requests; want <= %d", got, rounds, concurrency, concurrency)
	}
}
