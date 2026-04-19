package appkey

import (
	"sync"
	"sync/atomic"
	"time"
)

// ModelUsage tracks per-model counters for a single application key.
type ModelUsage struct {
	Requests         atomic.Int64
	PromptTokens     atomic.Int64
	CompletionTokens atomic.Int64
}

// UsageRecord holds all counters for a single application key.
type UsageRecord struct {
	Key       string
	Label     string
	CreatedAt time.Time

	TotalRequests     atomic.Int64
	StreamRequests    atomic.Int64
	NonStreamRequests atomic.Int64
	LastAccessedAt    atomic.Int64 // UnixNano; 0 means never accessed

	mu         sync.RWMutex
	modelUsage map[string]*ModelUsage
}

func (u *UsageRecord) getOrCreateModel(model string) *ModelUsage {
	u.mu.RLock()
	mu, ok := u.modelUsage[model]
	u.mu.RUnlock()
	if ok {
		return mu
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if mu, ok = u.modelUsage[model]; !ok {
		mu = &ModelUsage{}
		u.modelUsage[model] = mu
	}
	return mu
}

// Store holds all provisioned application keys and their usage counters.
// All methods are safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	records map[string]*UsageRecord
	onVend  func(*UsageSnapshot) // optional callback fired after Vend
}

// NewStore returns an empty Store.
func NewStore() *Store {
	return &Store{records: make(map[string]*UsageRecord)}
}

// SetOnVend registers a callback invoked synchronously after a key is vended.
// Intended for persistence layers to immediately write newly created keys.
// Must be called during initialization, before the server starts accepting
// traffic — the write to s.onVend is not protected by a lock.
func (s *Store) SetOnVend(fn func(*UsageSnapshot)) {
	s.onVend = fn
}

// Provision registers a pre-configured key. Idempotent — calling with the
// same key twice is a no-op.
func (s *Store) Provision(key, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.records[key]; !exists {
		s.records[key] = &UsageRecord{
			Key:        key,
			Label:      label,
			CreatedAt:  time.Now(),
			modelUsage: make(map[string]*ModelUsage),
		}
	}
}

// Vend generates a new key, provisions it in the store, and returns the record.
// If an onVend callback is registered, it is called synchronously after the
// record is created (used by the persistence layer to write immediately).
func (s *Store) Vend(label string) (*UsageRecord, error) {
	key, err := Generate()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	record := &UsageRecord{
		Key:        key,
		Label:      label,
		CreatedAt:  time.Now(),
		modelUsage: make(map[string]*ModelUsage),
	}
	s.records[key] = record
	onVend := s.onVend
	s.mu.Unlock()

	if onVend != nil {
		onVend(record.Snapshot())
	}
	return record, nil
}

// Lookup returns the UsageRecord for the given key, or nil if unknown.
func (s *Store) Lookup(key string) *UsageRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records[key]
}

// List returns snapshots of all provisioned keys.
func (s *Store) List() []*UsageSnapshot {
	s.mu.RLock()
	records := make([]*UsageRecord, 0, len(s.records))
	for _, r := range s.records {
		records = append(records, r)
	}
	s.mu.RUnlock()

	snapshots := make([]*UsageSnapshot, len(records))
	for i, r := range records {
		snapshots[i] = r.Snapshot()
	}
	return snapshots
}

// RecordRequest increments counters for key. Safe to call concurrently.
// promptTokens and completionTokens may be 0 (e.g. for streaming requests).
func (s *Store) RecordRequest(key, model string, stream bool, promptTokens, completionTokens int64) {
	rec := s.Lookup(key)
	if rec == nil {
		return
	}
	rec.TotalRequests.Add(1)
	rec.LastAccessedAt.Store(time.Now().UnixNano())
	if stream {
		rec.StreamRequests.Add(1)
	} else {
		rec.NonStreamRequests.Add(1)
	}
	if model != "" {
		mu := rec.getOrCreateModel(model)
		mu.Requests.Add(1)
		if promptTokens > 0 {
			mu.PromptTokens.Add(promptTokens)
		}
		if completionTokens > 0 {
			mu.CompletionTokens.Add(completionTokens)
		}
	}
}

// UsageSnapshot is a point-in-time, JSON-serializable view of a UsageRecord.
type UsageSnapshot struct {
	Key               string                    `json:"key"`
	Label             string                    `json:"label"`
	CreatedAt         time.Time                 `json:"created_at"`
	LastAccessedAt    *time.Time                `json:"last_accessed_at,omitempty"`
	TotalRequests     int64                     `json:"total_requests"`
	StreamRequests    int64                     `json:"stream_requests"`
	NonStreamRequests int64                     `json:"non_stream_requests"`
	Models            map[string]*ModelSnapshot `json:"models,omitempty"`
}

// ModelSnapshot is a JSON-serializable view of a ModelUsage.
type ModelSnapshot struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
}

// Restore reconstructs a UsageRecord from a persisted snapshot, or merges
// persisted counters into an existing record. Must be called before the
// server starts accepting requests.
//
// If the key already exists (e.g. provisioned from config), Restore merges
// the persisted counters and creation time into the existing record while
// preserving the existing label — config is authoritative for labels, bbolt
// is authoritative for counters.
func (s *Store) Restore(snap *UsageSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, exists := s.records[snap.Key]; exists {
		// Merge persisted counters; keep existing label.
		rec.CreatedAt = snap.CreatedAt
		if snap.LastAccessedAt != nil {
			rec.LastAccessedAt.Store(snap.LastAccessedAt.UnixNano())
		}
		rec.TotalRequests.Store(snap.TotalRequests)
		rec.StreamRequests.Store(snap.StreamRequests)
		rec.NonStreamRequests.Store(snap.NonStreamRequests)
		rec.mu.Lock()
		for model, ms := range snap.Models {
			mu := &ModelUsage{}
			mu.Requests.Store(ms.Requests)
			mu.PromptTokens.Store(ms.PromptTokens)
			mu.CompletionTokens.Store(ms.CompletionTokens)
			rec.modelUsage[model] = mu
		}
		rec.mu.Unlock()
		return
	}
	rec := &UsageRecord{
		Key:        snap.Key,
		Label:      snap.Label,
		CreatedAt:  snap.CreatedAt,
		modelUsage: make(map[string]*ModelUsage),
	}
	if snap.LastAccessedAt != nil {
		rec.LastAccessedAt.Store(snap.LastAccessedAt.UnixNano())
	}
	rec.TotalRequests.Store(snap.TotalRequests)
	rec.StreamRequests.Store(snap.StreamRequests)
	rec.NonStreamRequests.Store(snap.NonStreamRequests)
	for model, ms := range snap.Models {
		mu := &ModelUsage{}
		mu.Requests.Store(ms.Requests)
		mu.PromptTokens.Store(ms.PromptTokens)
		mu.CompletionTokens.Store(ms.CompletionTokens)
		rec.modelUsage[model] = mu
	}
	s.records[snap.Key] = rec
}

// Snapshot returns a consistent point-in-time view of the record.
func (u *UsageRecord) Snapshot() *UsageSnapshot {
	u.mu.RLock()
	models := make(map[string]*ModelSnapshot, len(u.modelUsage))
	for model, mu := range u.modelUsage {
		models[model] = &ModelSnapshot{
			Requests:         mu.Requests.Load(),
			PromptTokens:     mu.PromptTokens.Load(),
			CompletionTokens: mu.CompletionTokens.Load(),
		}
	}
	u.mu.RUnlock()
	snap := &UsageSnapshot{
		Key:               u.Key,
		Label:             u.Label,
		CreatedAt:         u.CreatedAt,
		TotalRequests:     u.TotalRequests.Load(),
		StreamRequests:    u.StreamRequests.Load(),
		NonStreamRequests: u.NonStreamRequests.Load(),
		Models:            models,
	}
	if ns := u.LastAccessedAt.Load(); ns != 0 {
		t := time.Unix(0, ns)
		snap.LastAccessedAt = &t
	}
	return snap
}
