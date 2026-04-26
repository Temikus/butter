package appkey

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestPersisterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	// Phase 1: create store, provision keys, record requests, flush.
	store1 := NewStore()
	store1.Provision("btr_testkey00000000001", "svc-alpha")
	store1.Provision("btr_testkey00000000002", "svc-beta")
	store1.RecordRequest("btr_testkey00000000001", "gpt-4o", false, 100, 50)
	store1.RecordRequest("btr_testkey00000000001", "gpt-4o", true, 0, 0)
	store1.RecordRequest("btr_testkey00000000002", "claude-3", false, 200, 80)

	p1, err := NewPersister(dbPath, store1, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p1.Start()
	if err := p1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Phase 2: new store, load from same db, verify.
	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister (reload): %v", err)
	}
	defer func() { _ = p2.Close() }()

	snaps := store2.List()
	if len(snaps) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(snaps))
	}

	rec := store2.Lookup("btr_testkey00000000001")
	if rec == nil {
		t.Fatal("expected key1 to be restored")
	}
	snap := rec.Snapshot()
	if snap.TotalRequests != 2 {
		t.Errorf("expected 2 total requests, got %d", snap.TotalRequests)
	}
	if snap.StreamRequests != 1 {
		t.Errorf("expected 1 stream request, got %d", snap.StreamRequests)
	}
	if snap.NonStreamRequests != 1 {
		t.Errorf("expected 1 non-stream request, got %d", snap.NonStreamRequests)
	}
	m, ok := snap.Models["gpt-4o"]
	if !ok {
		t.Fatal("expected gpt-4o model")
	}
	if m.PromptTokens != 100 || m.CompletionTokens != 50 {
		t.Errorf("unexpected tokens: prompt=%d completion=%d", m.PromptTokens, m.CompletionTokens)
	}
	if snap.LastAccessedAt == nil {
		t.Error("expected last_accessed_at to be set after RecordRequest")
	}

	rec2 := store2.Lookup("btr_testkey00000000002")
	if rec2 == nil {
		t.Fatal("expected key2 to be restored")
	}
	snap2 := rec2.Snapshot()
	if snap2.TotalRequests != 1 {
		t.Errorf("expected 1 total request for key2, got %d", snap2.TotalRequests)
	}
	if snap2.Label != "svc-beta" {
		t.Errorf("expected label 'svc-beta', got %q", snap2.Label)
	}
}

func TestPersisterStartupMerge(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	// Phase 1: persist one key.
	store1 := NewStore()
	store1.Provision("btr_testkey00000000001", "persisted")
	store1.RecordRequest("btr_testkey00000000001", "gpt-4o", false, 10, 5)

	p1, err := NewPersister(dbPath, store1, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p1.Start()
	if err := p1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Phase 2: load, then provision a config-defined key.
	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister (reload): %v", err)
	}
	defer func() { _ = p2.Close() }()

	// Simulate config provisioning after load.
	store2.Provision("btr_testkey00000000002", "from-config")

	// Both keys should exist.
	if store2.Lookup("btr_testkey00000000001") == nil {
		t.Error("persisted key missing")
	}
	if store2.Lookup("btr_testkey00000000002") == nil {
		t.Error("config key missing")
	}

	// Persisted key should have its counters.
	snap := store2.Lookup("btr_testkey00000000001").Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request from persisted key, got %d", snap.TotalRequests)
	}
}

func TestPersisterIdempotentRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	// Phase 1: persist a key with counters.
	store1 := NewStore()
	store1.Provision("btr_testkey00000000001", "original")
	store1.RecordRequest("btr_testkey00000000001", "gpt-4o", false, 100, 50)

	p1, err := NewPersister(dbPath, store1, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p1.Start()
	if err := p1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Phase 2: pre-provision the same key with a new label (simulating
	// config + bbolt overlap — matches production startup order in main.go).
	store2 := NewStore()
	store2.Provision("btr_testkey00000000001", "config-label")

	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister (reload): %v", err)
	}
	defer func() { _ = p2.Close() }()

	snap := store2.Lookup("btr_testkey00000000001").Snapshot()
	// Config label is preserved (config is authoritative for labels).
	if snap.Label != "config-label" {
		t.Errorf("expected config label 'config-label', got %q", snap.Label)
	}
	// Persisted counters are merged in (bbolt is authoritative for counters).
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 total request from bbolt, got %d", snap.TotalRequests)
	}
	m, ok := snap.Models["gpt-4o"]
	if !ok {
		t.Fatal("expected gpt-4o model counters from bbolt")
	}
	if m.PromptTokens != 100 || m.CompletionTokens != 50 {
		t.Errorf("unexpected tokens: prompt=%d completion=%d", m.PromptTokens, m.CompletionTokens)
	}
}

func TestPersisterPeriodicFlush(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	store := NewStore()
	store.Provision("btr_testkey00000000001", "svc")

	p, err := NewPersister(dbPath, store, 20*time.Millisecond, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p.Start()

	// Record requests after persister started.
	store.RecordRequest("btr_testkey00000000001", "gpt-4o", false, 10, 5)
	store.RecordRequest("btr_testkey00000000001", "gpt-4o", true, 0, 0)

	// Wait for at least one flush cycle.
	time.Sleep(60 * time.Millisecond)

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify by loading into a new store.
	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister (reload): %v", err)
	}
	defer func() { _ = p2.Close() }()

	snap := store2.Lookup("btr_testkey00000000001").Snapshot()
	if snap.TotalRequests != 2 {
		t.Errorf("expected 2 requests after periodic flush, got %d", snap.TotalRequests)
	}
}

func TestPersisterGracefulClose(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	store := NewStore()
	store.Provision("btr_testkey00000000001", "svc")
	store.RecordRequest("btr_testkey00000000001", "gpt-4o", false, 10, 5)

	// Use a very long interval so periodic flush won't fire.
	p, err := NewPersister(dbPath, store, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p.Start()

	// Close should do a final flush.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify the final flush happened.
	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister (reload): %v", err)
	}
	defer func() { _ = p2.Close() }()

	snap := store2.Lookup("btr_testkey00000000001").Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request from final flush, got %d", snap.TotalRequests)
	}
}

func TestPersisterEmptyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brand-new.db")
	logger := newTestLogger()

	store := NewStore()
	p, err := NewPersister(dbPath, store, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister on fresh path: %v", err)
	}
	defer func() { _ = p.Close() }()

	if len(store.List()) != 0 {
		t.Error("expected empty store from fresh db")
	}
}

func TestPersisterCorruptEntry(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	// Phase 1: write a valid key and a corrupt entry directly.
	store1 := NewStore()
	store1.Provision("btr_testkey00000000001", "good")

	p1, err := NewPersister(dbPath, store1, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p1.Start()
	if err := p1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Inject a corrupt entry directly into bbolt.
	db, err := openTestDB(dbPath)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketName).Put([]byte("btr_corrupt00000000000"), []byte("not-json{{{"))
	}); err != nil {
		t.Fatalf("inject corrupt: %v", err)
	}
	_ = db.Close()

	// Phase 2: load should skip the corrupt entry without failing.
	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister should not fail on corrupt entry: %v", err)
	}
	defer func() { _ = p2.Close() }()

	if store2.Lookup("btr_testkey00000000001") == nil {
		t.Error("valid key should still be loaded")
	}
	// Corrupt entry is skipped (not restored).
	if store2.Lookup("btr_corrupt00000000000") != nil {
		t.Error("corrupt entry should not be in store")
	}
}

func TestPersisterCloseNil(t *testing.T) {
	var p *Persister
	if err := p.Close(); err != nil {
		t.Errorf("Close on nil persister should return nil, got: %v", err)
	}
}

func TestPersisterCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	store := NewStore()
	p, err := NewPersister(dbPath, store, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p.Start()

	if err := p.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close should not fail: %v", err)
	}
}

func TestStoreRestore(t *testing.T) {
	s := NewStore()
	snap := &UsageSnapshot{
		Key:               "btr_restored0000000000",
		Label:             "restored-svc",
		CreatedAt:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		TotalRequests:     42,
		StreamRequests:    10,
		NonStreamRequests: 32,
		Models: map[string]*ModelSnapshot{
			"gpt-4o": {Requests: 30, PromptTokens: 1000, CompletionTokens: 500},
			"claude": {Requests: 12, PromptTokens: 200, CompletionTokens: 100},
		},
	}

	s.Restore(snap)

	rec := s.Lookup("btr_restored0000000000")
	if rec == nil {
		t.Fatal("restored key not found")
	}
	if rec.Label != "restored-svc" {
		t.Errorf("label: got %q, want 'restored-svc'", rec.Label)
	}
	got := rec.Snapshot()
	if got.TotalRequests != 42 {
		t.Errorf("total: got %d, want 42", got.TotalRequests)
	}
	if got.StreamRequests != 10 {
		t.Errorf("stream: got %d, want 10", got.StreamRequests)
	}
	if got.NonStreamRequests != 32 {
		t.Errorf("non-stream: got %d, want 32", got.NonStreamRequests)
	}
	if len(got.Models) != 2 {
		t.Fatalf("models: got %d, want 2", len(got.Models))
	}
	if got.Models["gpt-4o"].PromptTokens != 1000 {
		t.Errorf("gpt-4o prompt tokens: got %d, want 1000", got.Models["gpt-4o"].PromptTokens)
	}
}

func TestStoreRestoreMergesCounters(t *testing.T) {
	s := NewStore()
	s.Provision("btr_existing0000000000", "config-label")

	// Restore merges persisted counters but keeps existing label.
	snap := &UsageSnapshot{
		Key:               "btr_existing0000000000",
		Label:             "bbolt-label",
		CreatedAt:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		TotalRequests:     42,
		StreamRequests:    10,
		NonStreamRequests: 32,
		Models: map[string]*ModelSnapshot{
			"gpt-4o": {Requests: 30, PromptTokens: 1000, CompletionTokens: 500},
		},
	}
	s.Restore(snap)

	rec := s.Lookup("btr_existing0000000000")
	if rec.Label != "config-label" {
		t.Errorf("label should remain 'config-label', got %q", rec.Label)
	}
	got := rec.Snapshot()
	if got.TotalRequests != 42 {
		t.Errorf("total should be 42 from bbolt, got %d", got.TotalRequests)
	}
	if got.CreatedAt != snap.CreatedAt {
		t.Errorf("created_at should be restored from bbolt")
	}
	if got.Models["gpt-4o"].PromptTokens != 1000 {
		t.Errorf("model counters should be restored from bbolt")
	}
}

func TestPersisterVendPersist(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	store := NewStore()
	p, err := NewPersister(dbPath, store, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p.Start()

	// Vend a key — should be persisted immediately via onUpdate callback.
	rec, err := store.Vend("vended-svc", 0)
	if err != nil {
		t.Fatalf("Vend: %v", err)
	}
	vendedKey := rec.Key

	// Close without waiting for a flush tick — the key should already be in bbolt.
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify by loading into a new store.
	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister (reload): %v", err)
	}
	defer func() { _ = p2.Close() }()

	restored := store2.Lookup(vendedKey)
	if restored == nil {
		t.Fatal("vended key should be in bbolt immediately, not found after reload")
	}
	if restored.Label != "vended-svc" {
		t.Errorf("expected label 'vended-svc', got %q", restored.Label)
	}
}

// openTestDB is a helper to open bbolt directly for test setup.
func openTestDB(path string) (*bolt.DB, error) {
	return bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
}

func TestPersisterRevokeSurvives(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	store := NewStore()
	p, err := NewPersister(dbPath, store, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p.Start()

	rec, err := store.Vend("rotation-test", 0)
	if err != nil {
		t.Fatalf("Vend: %v", err)
	}
	if err := store.Revoke(rec.Key); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister reload: %v", err)
	}
	defer func() { _ = p2.Close() }()

	restored := store2.Lookup(rec.Key)
	if restored == nil {
		t.Fatal("vended+revoked key should be in bbolt")
	}
	if restored.RevokedAt.Load() == 0 {
		t.Error("RevokedAt should be restored as non-zero")
	}
	if store2.IsActive(restored) {
		t.Error("restored revoked key should be inactive")
	}
	if restored.Snapshot().Status != "revoked" {
		t.Errorf("expected status=revoked after restore, got %q", restored.Snapshot().Status)
	}
}

func TestPersisterFlushRevokeRace(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	logger := newTestLogger()

	store := NewStore()
	p, err := NewPersister(dbPath, store, time.Hour, logger)
	if err != nil {
		t.Fatalf("NewPersister: %v", err)
	}
	p.Start()

	rec, err := store.Vend("race-test", 0)
	if err != nil {
		t.Fatalf("Vend: %v", err)
	}
	for range 100 {
		store.RecordRequest(rec.Key, "gpt-4o", false, 1, 1)
	}

	// Run flush() and Revoke() concurrently many times. The mutex around
	// flush+PersistKey must ensure the final on-disk state is revoked, not
	// stale-pre-revoke.
	done := make(chan struct{})
	go func() {
		for range 20 {
			_ = p.flush()
		}
		close(done)
	}()
	if err := store.Revoke(rec.Key); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	<-done

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2 := NewStore()
	p2, err := NewPersister(dbPath, store2, time.Hour, logger)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer func() { _ = p2.Close() }()

	restored := store2.Lookup(rec.Key)
	if restored == nil || restored.RevokedAt.Load() == 0 {
		t.Errorf("revoke must survive flush/revoke race; got rec=%+v", restored)
	}
}
