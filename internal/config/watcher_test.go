package config

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"
)


const minimalConfig = `
server:
  address: ":8080"
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: sk-test
        weight: 1
routing:
  default_provider: openrouter
`

const minimalConfigAlt = `
server:
  address: ":8081"
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: sk-reloaded
        weight: 1
routing:
  default_provider: openrouter
`

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "butter-cfg-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	return f.Name()
}

func TestWatcherCallsOnChangeWhenFileModified(t *testing.T) {
	path := writeTempConfig(t, minimalConfig)

	changed := make(chan *Config, 1)
	w := NewWatcher(path, 10*time.Millisecond, newDiscardLogger(), func(cfg *Config) {
		changed <- cfg
	})
	w.Start()
	defer w.Stop()

	// Let the watcher seed its initial mtime.
	time.Sleep(30 * time.Millisecond)

	// Overwrite the file with different content and explicitly bump mtime
	// to avoid false negatives on filesystems with 1-second mtime granularity.
	if err := os.WriteFile(path, []byte(minimalConfigAlt), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	select {
	case cfg := <-changed:
		if cfg.Server.Address != ":8081" {
			t.Errorf("expected reloaded address :8081, got %s", cfg.Server.Address)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout: onChange was not called after file modification")
	}
}

func TestWatcherDoesNotCallOnChangeWhenFileUnchanged(t *testing.T) {
	path := writeTempConfig(t, minimalConfig)

	var calls atomic.Int32
	w := NewWatcher(path, 10*time.Millisecond, newDiscardLogger(), func(_ *Config) {
		calls.Add(1)
	})
	w.Start()
	defer w.Stop()

	// Let the watcher run several cycles without any file change.
	time.Sleep(80 * time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Errorf("expected 0 onChange calls for unchanged file, got %d", got)
	}
}

func TestWatcherSkipsReloadOnInvalidConfig(t *testing.T) {
	path := writeTempConfig(t, minimalConfig)

	var calls atomic.Int32
	w := NewWatcher(path, 10*time.Millisecond, newDiscardLogger(), func(_ *Config) {
		calls.Add(1)
	})
	w.Start()
	defer w.Stop()

	time.Sleep(30 * time.Millisecond)

	// Write deliberately malformed YAML (unclosed bracket).
	if err := os.WriteFile(path, []byte("key: [unclosed bracket"), 0644); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Errorf("expected 0 onChange calls for invalid config, got %d", got)
	}
}

func TestWatcherStopHaltsPolling(t *testing.T) {
	path := writeTempConfig(t, minimalConfig)

	var calls atomic.Int32
	w := NewWatcher(path, 10*time.Millisecond, newDiscardLogger(), func(_ *Config) {
		calls.Add(1)
	})
	w.Start()

	time.Sleep(30 * time.Millisecond)
	w.Stop()

	// Modify after Stop — onChange must not fire.
	if err := os.WriteFile(path, []byte(minimalConfigAlt), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Errorf("expected 0 calls after Stop, got %d", got)
	}
}

func TestWatcherStopIdempotent(t *testing.T) {
	path := writeTempConfig(t, minimalConfig)
	w := NewWatcher(path, 10*time.Millisecond, newDiscardLogger(), func(_ *Config) {})
	w.Start()
	// Multiple Stop calls must not panic.
	w.Stop()
	w.Stop()
}
