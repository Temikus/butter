package provider_test

import (
	"errors"
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

// A stream that keeps sending must outlive the timeout; only the wait for
// response headers is bounded.
func TestNewHTTPClient_StreamOutlivesTimeout(t *testing.T) {
	const timeout = 100 * time.Millisecond
	const chunk = "data: x\n\n"
	const chunks = 8

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		for range chunks {
			_, _ = io.WriteString(w, chunk)
			flusher.Flush()
			time.Sleep(timeout / 2)
		}
	}))
	defer srv.Close()

	client := provider.NewHTTPClient(timeout)
	defer client.CloseIdleConnections()

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("stream severed after %d bytes: %v", len(body), err)
	}
	if want := chunks * len(chunk); len(body) != want {
		t.Errorf("read %d bytes; want %d", len(body), want)
	}
}

// An upstream that sends headers and then stalls must not hold the read open.
func TestNewHTTPClient_BoundsBodyIdle(t *testing.T) {
	const timeout = 100 * time.Millisecond

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "data: x\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := provider.NewHTTPClient(timeout)
	defer client.CloseIdleConnections()

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	start := time.Now()
	_, err = io.ReadAll(resp.Body)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("got %v; want a timeout error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*timeout {
		t.Errorf("stalled body read took %v; want ~%v", elapsed, timeout)
	}
}

// An upstream that stops reading the request body must not hang the upload.
func TestNewHTTPClient_BoundsStalledUpload(t *testing.T) {
	const timeout = 100 * time.Millisecond

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release // never reads the body
	}))
	defer srv.Close()
	defer close(release)

	client := provider.NewHTTPClient(timeout)
	defer client.CloseIdleConnections()

	// Large enough to overflow loopback socket buffers.
	body := io.LimitReader(zeroReader{}, 64<<20)
	start := time.Now()
	resp, err := client.Post(srv.URL, "application/octet-stream", body)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected timeout on stalled upload")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("got %v; want a timeout error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*timeout {
		t.Errorf("stalled upload took %v; want ~%v", elapsed, timeout)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestNewHTTPClient_BoundsResponseHeaderWait(t *testing.T) {
	const timeout = 100 * time.Millisecond

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := provider.NewHTTPClient(timeout)
	defer client.CloseIdleConnections()

	start := time.Now()
	resp, err := client.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected timeout waiting for response headers")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("got %v; want a timeout error", err)
	}
	if elapsed := time.Since(start); elapsed > 10*timeout {
		t.Errorf("header wait took %v; want ~%v", elapsed, timeout)
	}
}
