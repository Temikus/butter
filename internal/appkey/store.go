package appkey

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnknownKey is returned by lifecycle operations on a key that has not
// been provisioned or vended.
var ErrUnknownKey = errors.New("app key not found")

// KeyScopes restricts which models and providers an app key may access.
// Nil or empty slices mean unrestricted.
type KeyScopes struct {
	AllowedModels    []string `json:"allowed_models,omitempty"`
	AllowedProviders []string `json:"allowed_providers,omitempty"`
}

// IsModelAllowed returns true if the scope permits the given model.
// Nil receiver or empty AllowedModels means all models are allowed.
func (ks *KeyScopes) IsModelAllowed(model string) bool {
	if ks == nil || len(ks.AllowedModels) == 0 {
		return true
	}
	for _, m := range ks.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

// IsProviderAllowed returns true if the scope permits the given provider.
// Nil receiver or empty AllowedProviders means all providers are allowed.
func (ks *KeyScopes) IsProviderAllowed(prov string) bool {
	if ks == nil || len(ks.AllowedProviders) == 0 {
		return true
	}
	for _, p := range ks.AllowedProviders {
		if p == prov {
			return true
		}
	}
	return false
}

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
	ExpiresAt         atomic.Int64 // UnixNano; 0 means never expires
	RevokedAt         atomic.Int64 // UnixNano; 0 means not revoked

	scopes atomic.Pointer[KeyScopes] // nil = unrestricted

	mu         sync.RWMutex
	modelUsage map[string]*ModelUsage
}

// GetScopes returns the key's scope restrictions, or nil if unrestricted.
// Atomic pointer load — safe for concurrent use without locks.
func (u *UsageRecord) GetScopes() *KeyScopes { return u.scopes.Load() }

// SetScopes atomically replaces the key's scope restrictions.
func (u *UsageRecord) SetScopes(s *KeyScopes) { u.scopes.Store(s) }

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
	mu       sync.RWMutex
	records  map[string]*UsageRecord
	onUpdate func(*UsageSnapshot) // fired after Vend/Revoke/Rotate/SetExpiry
	onDelete func(string)         // fired after Delete with the deleted key
	logger   *slog.Logger
}

// NewStore returns an empty Store. If logger is nil, slog.Default() is used.
func NewStore(logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{records: make(map[string]*UsageRecord), logger: logger}
}

// SetOnUpdate registers a callback invoked synchronously after any lifecycle
// mutation: Vend, Revoke, Rotate, SetExpiry. Intended for persistence layers
// to durably record the new state immediately. Must be called during
// initialization — the write is not protected by a lock.
func (s *Store) SetOnUpdate(fn func(*UsageSnapshot)) {
	s.onUpdate = fn
}

// SetOnDelete registers a callback invoked synchronously after Delete with the
// removed key. Intended for persistence layers to evict the key from durable
// storage. Distinct from onUpdate so callers don't have to overload the
// snapshot signal with a tombstone marker. Must be called during
// initialization.
func (s *Store) SetOnDelete(fn func(string)) {
	s.onDelete = fn
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

// VendOption configures optional fields on a newly vended key.
type VendOption func(*UsageRecord)

// WithScopes sets scope restrictions on a vended key.
func WithScopes(scopes KeyScopes) VendOption {
	return func(r *UsageRecord) { r.scopes.Store(&scopes) }
}

// Vend generates a new key, provisions it in the store, and returns the record.
// If ttl is non-zero, the key's ExpiresAt is set to CreatedAt + ttl.
// If an onUpdate callback is registered, it is called synchronously after the
// record is created (used by the persistence layer to write immediately).
func (s *Store) Vend(label string, ttl time.Duration, opts ...VendOption) (*UsageRecord, error) {
	key, err := Generate()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s.mu.Lock()
	record := &UsageRecord{
		Key:        key,
		Label:      label,
		CreatedAt:  now,
		modelUsage: make(map[string]*ModelUsage),
	}
	if ttl > 0 {
		record.ExpiresAt.Store(now.Add(ttl).UnixNano())
	}
	for _, opt := range opts {
		opt(record)
	}
	s.records[key] = record
	onUpdate := s.onUpdate
	s.mu.Unlock()

	if onUpdate != nil {
		onUpdate(record.Snapshot())
	}

	logAttrs := []any{"event", "appkey.vend", "key", key, "label", label}
	if ttl > 0 {
		logAttrs = append(logAttrs, "expires_at", now.Add(ttl))
	}
	s.logger.Info("app key vended", logAttrs...)

	return record, nil
}

// IsActive reports whether rec is currently usable: not revoked and not past
// its expiry. Pure atomic read; no lock.
func (s *Store) IsActive(rec *UsageRecord) bool {
	if rec == nil {
		return false
	}
	if rec.RevokedAt.Load() != 0 {
		return false
	}
	if exp := rec.ExpiresAt.Load(); exp != 0 && time.Now().UnixNano() >= exp {
		return false
	}
	return true
}

// Revoke marks key as revoked. Idempotent: if already revoked, the original
// timestamp is preserved. Returns ErrUnknownKey if key is not provisioned.
func (s *Store) Revoke(key string) error {
	rec := s.Lookup(key)
	if rec == nil {
		return ErrUnknownKey
	}
	if rec.RevokedAt.CompareAndSwap(0, time.Now().UnixNano()) {
		if s.onUpdate != nil {
			s.onUpdate(rec.Snapshot())
		}
		s.logger.Info("app key revoked", "event", "appkey.revoke", "key", key)
	}
	return nil
}

// SetExpiry sets the expiry timestamp on key. A zero time clears expiry.
// Past timestamps are accepted (the key becomes inactive on next Lookup-time
// check). Returns ErrUnknownKey if key is not provisioned.
func (s *Store) SetExpiry(key string, expiresAt time.Time) error {
	rec := s.Lookup(key)
	if rec == nil {
		return ErrUnknownKey
	}
	if expiresAt.IsZero() {
		rec.ExpiresAt.Store(0)
	} else {
		rec.ExpiresAt.Store(expiresAt.UnixNano())
	}
	if s.onUpdate != nil {
		s.onUpdate(rec.Snapshot())
	}

	if expiresAt.IsZero() {
		s.logger.Info("app key expiry cleared", "event", "appkey.set_expiry", "key", key)
	} else {
		s.logger.Info("app key expiry updated", "event", "appkey.set_expiry", "key", key, "expires_at", expiresAt)
	}

	return nil
}

// Rotate vends a new key adopting the old key's label (or newLabel if non-empty)
// and revokes the old key. Returns the snapshots for both keys, or ErrUnknownKey
// if the old key is not provisioned. Rotating an already-revoked or expired key
// succeeds (rotation is a recovery operation).
//
// TTL inheritance: if the old key has a future expiry, the new key inherits the
// remaining duration so rotation does not silently extend the key's lifetime.
// If the old key has no expiry or is already past expiry, the new key has no
// expiry (operators can set one explicitly via SetExpiry).
func (s *Store) Rotate(oldKey, newLabel string) (oldSnap, newSnap *UsageSnapshot, err error) {
	old := s.Lookup(oldKey)
	if old == nil {
		return nil, nil, ErrUnknownKey
	}
	label := old.Label
	if newLabel != "" {
		label = newLabel
	}
	var ttl time.Duration
	if exp := old.ExpiresAt.Load(); exp != 0 {
		if remaining := time.Duration(exp - time.Now().UnixNano()); remaining > 0 {
			ttl = remaining
		}
	}
	var opts []VendOption
	if sc := old.GetScopes(); sc != nil {
		opts = append(opts, WithScopes(*sc))
	}
	newRec, err := s.Vend(label, ttl, opts...)
	if err != nil {
		return nil, nil, err
	}
	if old.RevokedAt.CompareAndSwap(0, time.Now().UnixNano()) {
		if s.onUpdate != nil {
			s.onUpdate(old.Snapshot())
		}
	}

	s.logger.Info("app key rotated", "event", "appkey.rotate", "old_key", oldKey, "new_key", newRec.Key, "label", label)

	return old.Snapshot(), newRec.Snapshot(), nil
}

// UpdateScopes atomically replaces the scope restrictions on key. Passing nil
// clears all restrictions (unrestricted). Returns ErrUnknownKey if key is not
// provisioned. Fires onUpdate for persistence.
func (s *Store) UpdateScopes(key string, scopes *KeyScopes) error {
	rec := s.Lookup(key)
	if rec == nil {
		return ErrUnknownKey
	}
	rec.scopes.Store(scopes)
	if s.onUpdate != nil {
		s.onUpdate(rec.Snapshot())
	}
	if scopes != nil {
		s.logger.Info("app key scopes updated",
			"event", "appkey.set_scopes",
			"key", key,
			"allowed_models", scopes.AllowedModels,
			"allowed_providers", scopes.AllowedProviders,
		)
	} else {
		s.logger.Info("app key scopes cleared", "event", "appkey.set_scopes", "key", key)
	}
	return nil
}

// Lookup returns the UsageRecord for the given key, or nil if unknown.
func (s *Store) Lookup(key string) *UsageRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.records[key]
}

// Delete fully removes key and all of its usage history from the store.
// Distinct from Revoke, which preserves the record. Returns ErrUnknownKey if
// key is not provisioned. After Delete, in-flight async RecordRequest calls
// for this key become no-ops via Lookup→nil — usage updates that race with
// Delete are silently dropped, mirroring the existing "RecordRequest never
// blocks the response path" guarantee.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	if _, exists := s.records[key]; !exists {
		s.mu.Unlock()
		return ErrUnknownKey
	}
	delete(s.records, key)
	onDelete := s.onDelete
	s.mu.Unlock()

	if onDelete != nil {
		onDelete(key)
	}

	s.logger.Info("app key purged", "event", "appkey.delete", "key", key)

	return nil
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
	Status            string                    `json:"status"` // "active" | "revoked" | "expired"
	CreatedAt         time.Time                 `json:"created_at"`
	LastAccessedAt    *time.Time                `json:"last_accessed_at,omitempty"`
	ExpiresAt         *time.Time                `json:"expires_at,omitempty"`
	RevokedAt         *time.Time                `json:"revoked_at,omitempty"`
	TotalRequests     int64                     `json:"total_requests"`
	StreamRequests    int64                     `json:"stream_requests"`
	NonStreamRequests int64                     `json:"non_stream_requests"`
	Models            map[string]*ModelSnapshot `json:"models,omitempty"`
	Scopes            *KeyScopes                `json:"scopes,omitempty"`
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
		if snap.ExpiresAt != nil {
			rec.ExpiresAt.Store(snap.ExpiresAt.UnixNano())
		}
		if snap.RevokedAt != nil {
			rec.RevokedAt.Store(snap.RevokedAt.UnixNano())
		}
		rec.TotalRequests.Store(snap.TotalRequests)
		rec.StreamRequests.Store(snap.StreamRequests)
		rec.NonStreamRequests.Store(snap.NonStreamRequests)
		if snap.Scopes != nil {
			rec.scopes.Store(snap.Scopes)
		}
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
	if snap.ExpiresAt != nil {
		rec.ExpiresAt.Store(snap.ExpiresAt.UnixNano())
	}
	if snap.RevokedAt != nil {
		rec.RevokedAt.Store(snap.RevokedAt.UnixNano())
	}
	if snap.Scopes != nil {
		rec.scopes.Store(snap.Scopes)
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
	if ns := u.ExpiresAt.Load(); ns != 0 {
		t := time.Unix(0, ns)
		snap.ExpiresAt = &t
	}
	if ns := u.RevokedAt.Load(); ns != 0 {
		t := time.Unix(0, ns)
		snap.RevokedAt = &t
	}
	if sc := u.scopes.Load(); sc != nil {
		snap.Scopes = &KeyScopes{
			AllowedModels:    sc.AllowedModels,
			AllowedProviders: sc.AllowedProviders,
		}
	}
	snap.Status = u.computeStatus()
	return snap
}

// computeStatus returns "revoked", "expired", or "active" based on the
// record's lifecycle atomics.
func (u *UsageRecord) computeStatus() string {
	if u.RevokedAt.Load() != 0 {
		return "revoked"
	}
	if exp := u.ExpiresAt.Load(); exp != 0 && time.Now().UnixNano() >= exp {
		return "expired"
	}
	return "active"
}
