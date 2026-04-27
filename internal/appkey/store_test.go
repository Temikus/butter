package appkey

import (
	"errors"
	"testing"
	"time"
)

func TestStoreProvisionAndLookup(t *testing.T) {
	s := NewStore()
	s.Provision("btr_testkey00000000000", "my-service")

	rec := s.Lookup("btr_testkey00000000000")
	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.Key != "btr_testkey00000000000" {
		t.Errorf("unexpected key: %s", rec.Key)
	}
	if rec.Label != "my-service" {
		t.Errorf("unexpected label: %s", rec.Label)
	}
}

func TestStoreProvisionIdempotent(t *testing.T) {
	s := NewStore()
	s.Provision("btr_testkey00000000000", "first")
	s.Provision("btr_testkey00000000000", "second") // should not overwrite

	rec := s.Lookup("btr_testkey00000000000")
	if rec.Label != "first" {
		t.Errorf("expected label 'first', got %q", rec.Label)
	}
}

func TestStoreLookupUnknown(t *testing.T) {
	s := NewStore()
	if got := s.Lookup("btr_notprovisioned000"); got != nil {
		t.Errorf("expected nil for unknown key, got %v", got)
	}
}

func TestStoreVend(t *testing.T) {
	s := NewStore()
	rec, err := s.Vend("test-label", 0)
	if err != nil {
		t.Fatalf("Vend() error: %v", err)
	}
	if !IsValid(rec.Key) {
		t.Errorf("vended key %q is not valid", rec.Key)
	}
	if rec.Label != "test-label" {
		t.Errorf("unexpected label: %s", rec.Label)
	}

	// Key should be in store.
	if s.Lookup(rec.Key) == nil {
		t.Error("vended key not found in store")
	}
}

func TestStoreRecordRequest(t *testing.T) {
	s := NewStore()
	s.Provision("btr_testkey00000000000", "svc")

	s.RecordRequest("btr_testkey00000000000", "gpt-4o", false, 100, 50)
	s.RecordRequest("btr_testkey00000000000", "gpt-4o", true, 0, 0)

	snap := s.Lookup("btr_testkey00000000000").Snapshot()
	if snap.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", snap.TotalRequests)
	}
	if snap.NonStreamRequests != 1 {
		t.Errorf("expected 1 non-stream, got %d", snap.NonStreamRequests)
	}
	if snap.StreamRequests != 1 {
		t.Errorf("expected 1 stream, got %d", snap.StreamRequests)
	}

	m, ok := snap.Models["gpt-4o"]
	if !ok {
		t.Fatal("expected gpt-4o in models")
	}
	if m.Requests != 2 {
		t.Errorf("expected 2 model requests, got %d", m.Requests)
	}
	if m.PromptTokens != 100 {
		t.Errorf("expected 100 prompt tokens, got %d", m.PromptTokens)
	}
	if m.CompletionTokens != 50 {
		t.Errorf("expected 50 completion tokens, got %d", m.CompletionTokens)
	}
}

func TestStoreRecordRequestSetsLastAccessed(t *testing.T) {
	s := NewStore()
	s.Provision("btr_testkey00000000000", "svc")

	// Before any request, LastAccessedAt should be nil.
	snap := s.Lookup("btr_testkey00000000000").Snapshot()
	if snap.LastAccessedAt != nil {
		t.Error("expected nil last_accessed_at before any request")
	}

	s.RecordRequest("btr_testkey00000000000", "gpt-4o", false, 10, 5)

	snap = s.Lookup("btr_testkey00000000000").Snapshot()
	if snap.LastAccessedAt == nil {
		t.Fatal("expected last_accessed_at to be set after RecordRequest")
	}
}

func TestStoreRecordRequestUnknownKey(t *testing.T) {
	s := NewStore()
	// Should not panic on unknown key.
	s.RecordRequest("btr_unknown00000000000", "gpt-4o", false, 10, 5)
}

func TestStoreList(t *testing.T) {
	s := NewStore()
	s.Provision("btr_testkey00000000001", "svc-1")
	s.Provision("btr_testkey00000000002", "svc-2")

	list := s.List()
	if len(list) != 2 {
		t.Errorf("expected 2 entries, got %d", len(list))
	}
}

func TestStoreVendWithTTL(t *testing.T) {
	s := NewStore()
	rec, err := s.Vend("ephemeral", time.Hour)
	if err != nil {
		t.Fatalf("Vend: %v", err)
	}
	if rec.ExpiresAt.Load() == 0 {
		t.Error("expected ExpiresAt to be set when ttl > 0")
	}
	snap := rec.Snapshot()
	if snap.ExpiresAt == nil {
		t.Error("expected snapshot.ExpiresAt to be non-nil")
	}
	if snap.Status != "active" {
		t.Errorf("expected status=active, got %q", snap.Status)
	}
}

func TestStoreIsActive(t *testing.T) {
	s := NewStore()
	s.Provision("btr_active000000000000a", "svc")
	if rec := s.Lookup("btr_active000000000000a"); !s.IsActive(rec) {
		t.Error("fresh key should be active")
	}

	s.Provision("btr_expired00000000000a", "svc")
	rec := s.Lookup("btr_expired00000000000a")
	rec.ExpiresAt.Store(time.Now().Add(-time.Hour).UnixNano())
	if s.IsActive(rec) {
		t.Error("expired key should not be active")
	}
	if rec.Snapshot().Status != "expired" {
		t.Errorf("expected status=expired, got %q", rec.Snapshot().Status)
	}

	if s.IsActive(nil) {
		t.Error("nil record should not be active")
	}
}

func TestStoreRevoke(t *testing.T) {
	s := NewStore()
	s.Provision("btr_revoke0000000000000", "svc")

	if err := s.Revoke("btr_revoke0000000000000"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	rec := s.Lookup("btr_revoke0000000000000")
	if s.IsActive(rec) {
		t.Error("revoked key should be inactive")
	}
	if rec.Snapshot().Status != "revoked" {
		t.Errorf("expected status=revoked, got %q", rec.Snapshot().Status)
	}

	// Idempotent: original timestamp preserved.
	originalTs := rec.RevokedAt.Load()
	if err := s.Revoke("btr_revoke0000000000000"); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	if rec.RevokedAt.Load() != originalTs {
		t.Error("Revoke should be idempotent — original timestamp must be preserved")
	}

	// Unknown key.
	if err := s.Revoke("btr_unknown0000000000a"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("expected ErrUnknownKey, got %v", err)
	}
}

func TestStoreSetExpiry(t *testing.T) {
	s := NewStore()
	s.Provision("btr_setexp00000000000a", "svc")

	future := time.Now().Add(time.Hour)
	if err := s.SetExpiry("btr_setexp00000000000a", future); err != nil {
		t.Fatalf("SetExpiry: %v", err)
	}
	rec := s.Lookup("btr_setexp00000000000a")
	if !s.IsActive(rec) {
		t.Error("key with future expiry should be active")
	}

	// Past timestamp → inactive.
	if err := s.SetExpiry("btr_setexp00000000000a", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("SetExpiry past: %v", err)
	}
	if s.IsActive(rec) {
		t.Error("key with past expiry should be inactive")
	}

	// Zero time clears expiry.
	if err := s.SetExpiry("btr_setexp00000000000a", time.Time{}); err != nil {
		t.Fatalf("SetExpiry clear: %v", err)
	}
	if !s.IsActive(rec) {
		t.Error("clearing expiry should make key active again")
	}
	if rec.ExpiresAt.Load() != 0 {
		t.Error("ExpiresAt should be 0 after clearing")
	}

	if err := s.SetExpiry("btr_unknown0000000000a", future); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("expected ErrUnknownKey, got %v", err)
	}
}

func TestStoreRotate(t *testing.T) {
	s := NewStore()
	s.Provision("btr_rotate0000000000000", "production")
	s.RecordRequest("btr_rotate0000000000000", "gpt-4o", false, 10, 5)

	oldSnap, newSnap, err := s.Rotate("btr_rotate0000000000000", "")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if oldSnap.Status != "revoked" {
		t.Errorf("expected old key revoked, got status=%q", oldSnap.Status)
	}
	if oldSnap.TotalRequests != 1 {
		t.Errorf("expected old key's usage history preserved (got %d)", oldSnap.TotalRequests)
	}
	if newSnap.Label != "production" {
		t.Errorf("expected new key to inherit label, got %q", newSnap.Label)
	}
	if newSnap.Status != "active" {
		t.Errorf("expected new key active, got %q", newSnap.Status)
	}
	if newSnap.Key == oldSnap.Key {
		t.Error("new key must differ from old")
	}
	if !s.IsActive(s.Lookup(newSnap.Key)) {
		t.Error("new key should be active in store")
	}
	if s.IsActive(s.Lookup(oldSnap.Key)) {
		t.Error("old key should be inactive in store")
	}

	// Override label.
	_, newSnap2, err := s.Rotate(newSnap.Key, "staging")
	if err != nil {
		t.Fatalf("Rotate with label: %v", err)
	}
	if newSnap2.Label != "staging" {
		t.Errorf("expected label override, got %q", newSnap2.Label)
	}

	// Rotating an already-revoked key still succeeds (recovery).
	if _, _, err := s.Rotate(oldSnap.Key, ""); err != nil {
		t.Errorf("rotating revoked key should succeed, got %v", err)
	}

	if _, _, err := s.Rotate("btr_unknown0000000000a", ""); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("expected ErrUnknownKey, got %v", err)
	}
}

func TestStoreRotateInheritsTTL(t *testing.T) {
	s := NewStore()

	// Old key with future expiry → new key inherits remaining duration.
	rec, _ := s.Vend("with-ttl", time.Hour)
	oldExp := rec.ExpiresAt.Load()
	_, newSnap, err := s.Rotate(rec.Key, "")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if newSnap.ExpiresAt == nil {
		t.Fatal("rotated key should inherit TTL from old key")
	}
	// New expiry should be within ~1s of old expiry (some elapsed time between Vend and Rotate).
	skew := oldExp - newSnap.ExpiresAt.UnixNano()
	if skew < 0 {
		skew = -skew
	}
	if skew > int64(time.Second) {
		t.Errorf("rotated TTL drifted >1s from old expiry: skew=%v", time.Duration(skew))
	}

	// Old key with no expiry → new key gets no expiry.
	plain, _ := s.Vend("no-ttl", 0)
	_, plainNew, err := s.Rotate(plain.Key, "")
	if err != nil {
		t.Fatalf("Rotate plain: %v", err)
	}
	if plainNew.ExpiresAt != nil {
		t.Errorf("rotated key from no-TTL parent should have no expiry, got %v", plainNew.ExpiresAt)
	}

	// Old key already expired → new key gets no expiry (operator must explicitly SetExpiry).
	expired, _ := s.Vend("already-expired", 0)
	expired.ExpiresAt.Store(time.Now().Add(-time.Hour).UnixNano())
	_, expiredNew, err := s.Rotate(expired.Key, "")
	if err != nil {
		t.Fatalf("Rotate expired: %v", err)
	}
	if expiredNew.ExpiresAt != nil {
		t.Errorf("rotated key from expired parent should have no expiry, got %v", expiredNew.ExpiresAt)
	}
}

func TestStoreDelete(t *testing.T) {
	s := NewStore()
	rec, _ := s.Vend("purge-me", 0)
	s.RecordRequest(rec.Key, "gpt-4o", false, 100, 50)

	if err := s.Delete(rec.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := s.Lookup(rec.Key); got != nil {
		t.Errorf("expected nil after Delete, got %v", got)
	}
	// RecordRequest after delete must be a silent no-op (no panic, no resurrection).
	s.RecordRequest(rec.Key, "gpt-4o", false, 1, 1)
	if got := s.Lookup(rec.Key); got != nil {
		t.Error("RecordRequest must not resurrect a deleted key")
	}
}

func TestStoreDeleteUnknown(t *testing.T) {
	s := NewStore()
	if err := s.Delete("btr_neverexisted00000000"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("expected ErrUnknownKey, got %v", err)
	}
}

func TestStoreDeleteFiresHook(t *testing.T) {
	s := NewStore()
	var deletedKeys []string
	s.SetOnDelete(func(key string) {
		deletedKeys = append(deletedKeys, key)
	})

	rec, _ := s.Vend("svc", 0)
	if err := s.Delete(rec.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(deletedKeys) != 1 || deletedKeys[0] != rec.Key {
		t.Errorf("expected onDelete fired with %q once, got %v", rec.Key, deletedKeys)
	}

	// Unknown key must not fire the hook.
	deletedKeys = deletedKeys[:0]
	_ = s.Delete("btr_unknown00000000000a")
	if len(deletedKeys) != 0 {
		t.Errorf("onDelete should not fire on ErrUnknownKey, got %v", deletedKeys)
	}
}

func TestStoreOnUpdateCallback(t *testing.T) {
	s := NewStore()
	var updates []string
	s.SetOnUpdate(func(snap *UsageSnapshot) {
		updates = append(updates, snap.Status)
	})

	rec, _ := s.Vend("svc", 0)
	if err := s.Revoke(rec.Key); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := s.SetExpiry(rec.Key, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("SetExpiry: %v", err)
	}

	// Vend → active, Revoke → revoked, SetExpiry → revoked (still revoked).
	if len(updates) != 3 {
		t.Fatalf("expected 3 onUpdate fires, got %d: %v", len(updates), updates)
	}
	if updates[0] != "active" {
		t.Errorf("expected first update active, got %q", updates[0])
	}
	if updates[1] != "revoked" {
		t.Errorf("expected second update revoked, got %q", updates[1])
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore()
	s.Provision("btr_testkey00000000000", "svc")

	done := make(chan struct{})
	for range 50 {
		go func() {
			s.RecordRequest("btr_testkey00000000000", "gpt-4o", false, 1, 1)
			done <- struct{}{}
		}()
	}
	for range 50 {
		<-done
	}

	snap := s.Lookup("btr_testkey00000000000").Snapshot()
	if snap.TotalRequests != 50 {
		t.Errorf("expected 50, got %d", snap.TotalRequests)
	}
}
