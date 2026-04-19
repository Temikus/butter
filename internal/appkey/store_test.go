package appkey

import (
	"testing"
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
	rec, err := s.Vend("test-label")
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
