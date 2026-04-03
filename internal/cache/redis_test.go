package cache

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newTestRedis(t *testing.T) (*Redis, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc, err := NewRedis(mr.Addr(), "", 0, "butter:")
	if err != nil {
		t.Fatalf("NewRedis failed: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc, mr
}

func TestRedis_SetAndGet(t *testing.T) {
	rc, _ := newTestRedis(t)

	rc.Set("key1", []byte("value1"), 5*time.Minute)
	got := rc.Get("key1")
	if string(got) != "value1" {
		t.Errorf("expected value1, got %q", got)
	}
}

func TestRedis_GetMiss(t *testing.T) {
	rc, _ := newTestRedis(t)

	got := rc.Get("nonexistent")
	if got != nil {
		t.Errorf("expected nil for missing key, got %q", got)
	}
}

func TestRedis_TTLExpiry(t *testing.T) {
	rc, mr := newTestRedis(t)

	rc.Set("expiring", []byte("data"), 1*time.Second)

	got := rc.Get("expiring")
	if string(got) != "data" {
		t.Fatalf("expected data before expiry, got %q", got)
	}

	mr.FastForward(2 * time.Second)

	got = rc.Get("expiring")
	if got != nil {
		t.Errorf("expected nil after expiry, got %q", got)
	}
}

func TestRedis_Overwrite(t *testing.T) {
	rc, _ := newTestRedis(t)

	rc.Set("key", []byte("v1"), 5*time.Minute)
	rc.Set("key", []byte("v2"), 5*time.Minute)

	got := rc.Get("key")
	if string(got) != "v2" {
		t.Errorf("expected v2 after overwrite, got %q", got)
	}
}

func TestRedis_Len(t *testing.T) {
	rc, _ := newTestRedis(t)

	if rc.Len() != 0 {
		t.Errorf("expected 0 entries, got %d", rc.Len())
	}

	rc.Set("a", []byte("1"), 5*time.Minute)
	rc.Set("b", []byte("2"), 5*time.Minute)

	if rc.Len() != 2 {
		t.Errorf("expected 2 entries, got %d", rc.Len())
	}
}

func TestRedis_KeyPrefix(t *testing.T) {
	rc, mr := newTestRedis(t)

	rc.Set("mykey", []byte("data"), 5*time.Minute)

	// Verify the key is stored with prefix in Redis.
	if !mr.Exists("butter:mykey") {
		t.Error("expected key 'butter:mykey' to exist in Redis")
	}
	if mr.Exists("mykey") {
		t.Error("key without prefix should not exist")
	}
}

func TestNewRedis_ConnectionFailure(t *testing.T) {
	_, err := NewRedis("localhost:1", "", 0, "test:")
	if err == nil {
		t.Fatal("expected error for unreachable Redis")
	}
}
