package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/temikus/butter/internal/appkey"
)

// handleAppKeyCreate vends a new application key.
// POST /v1/app-keys
// Body (optional): {"label": "my-service", "ttl_seconds": 3600}
// ttl_seconds overrides the server's default TTL when present; 0 disables expiry.
func (s *Server) handleAppKeyCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label            string   `json:"label"`
		TTLSeconds       *int64   `json:"ttl_seconds,omitempty"`
		AllowedModels    []string `json:"allowed_models,omitempty"`
		AllowedProviders []string `json:"allowed_providers,omitempty"`
	}
	// Best-effort decode; all fields are optional.
	_ = json.NewDecoder(r.Body).Decode(&req)

	ttl := s.appKeyTTL
	if req.TTLSeconds != nil {
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}

	var opts []appkey.VendOption
	if len(req.AllowedModels) > 0 || len(req.AllowedProviders) > 0 {
		opts = append(opts, appkey.WithScopes(appkey.KeyScopes{
			AllowedModels:    req.AllowedModels,
			AllowedProviders: req.AllowedProviders,
		}))
	}

	record, err := s.appKeyStore.Vend(req.Label, ttl, opts...)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to generate key")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(record.Snapshot())
}

// handleAppKeyList returns all provisioned keys and their usage.
// GET /v1/app-keys
func (s *Server) handleAppKeyList(w http.ResponseWriter, _ *http.Request) {
	snapshots := s.appKeyStore.List()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snapshots)
}

// handleAppKeyUsage returns usage stats for a specific key.
// GET /v1/app-keys/{key}/usage
func (s *Server) handleAppKeyUsage(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	rec := s.appKeyStore.Lookup(key)
	if rec == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rec.Snapshot())
}

// handleAppKeyRevoke marks a key as revoked. Counters are preserved for audit.
// For full removal of the key and its history, use POST /v1/app-keys/{key}/purge.
// DELETE /v1/app-keys/{key}
func (s *Server) handleAppKeyRevoke(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := s.appKeyStore.Revoke(key); err != nil {
		if errors.Is(err, appkey.ErrUnknownKey) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to revoke key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAppKeyPurge fully removes a key and all of its usage history from
// both the in-memory store and any persistent backing store. Distinct from
// DELETE which only revokes.
//
// In-flight async RecordRequest calls for this key may still be queued when
// purge fires; they become no-ops via Lookup→nil — usage updates that race
// with purge are silently dropped.
// POST /v1/app-keys/{key}/purge
func (s *Server) handleAppKeyPurge(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := s.appKeyStore.Delete(key); err != nil {
		if errors.Is(err, appkey.ErrUnknownKey) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to purge key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAppKeyUpdate sets or clears the expiry on a key.
// PATCH /v1/app-keys/{key}
// Body: {"expires_at": "<RFC3339>"} or {"ttl_seconds": N}.
// Setting expires_at to null or ttl_seconds to 0 clears expiry. A past
// timestamp is accepted (the key becomes inactive immediately).
func (s *Server) handleAppKeyUpdate(w http.ResponseWriter, r *http.Request) { //nolint:gocyclo // handles multiple optional update fields (expiry/scopes) with validation
	key := r.PathValue("key")

	var req struct {
		ExpiresAt        json.RawMessage `json:"expires_at,omitempty"`
		TTLSeconds       *int64          `json:"ttl_seconds,omitempty"`
		AllowedModels    *[]string       `json:"allowed_models,omitempty"`
		AllowedProviders *[]string       `json:"allowed_providers,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.ExpiresAt) > 0 && req.TTLSeconds != nil {
		s.writeError(w, http.StatusBadRequest, "specify either expires_at or ttl_seconds, not both")
		return
	}

	// Update expiry if requested.
	hasExpiryUpdate := req.TTLSeconds != nil || len(req.ExpiresAt) > 0
	if hasExpiryUpdate {
		var expiry time.Time
		switch {
		case req.TTLSeconds != nil:
			if *req.TTLSeconds > 0 {
				expiry = time.Now().Add(time.Duration(*req.TTLSeconds) * time.Second)
			}
		case string(req.ExpiresAt) != "null":
			var ts time.Time
			if err := json.Unmarshal(req.ExpiresAt, &ts); err != nil {
				s.writeError(w, http.StatusBadRequest, "expires_at must be RFC3339 timestamp or null")
				return
			}
			expiry = ts
		}

		if err := s.appKeyStore.SetExpiry(key, expiry); err != nil {
			if errors.Is(err, appkey.ErrUnknownKey) {
				s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
				return
			}
			s.writeError(w, http.StatusInternalServerError, "failed to update key")
			return
		}
	}

	// Update scopes if requested. nil pointer = not sent, empty slice = clear restriction.
	if req.AllowedModels != nil || req.AllowedProviders != nil {
		rec := s.appKeyStore.Lookup(key)
		if rec == nil {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
			return
		}
		current := rec.GetScopes()
		scopes := &appkey.KeyScopes{}
		if current != nil {
			*scopes = *current
		}
		if req.AllowedModels != nil {
			scopes.AllowedModels = *req.AllowedModels
		}
		if req.AllowedProviders != nil {
			scopes.AllowedProviders = *req.AllowedProviders
		}
		if len(scopes.AllowedModels) == 0 && len(scopes.AllowedProviders) == 0 {
			scopes = nil
		}
		if err := s.appKeyStore.UpdateScopes(key, scopes); err != nil {
			if errors.Is(err, appkey.ErrUnknownKey) {
				s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
				return
			}
			s.writeError(w, http.StatusInternalServerError, "failed to update key scopes")
			return
		}
	}

	rec := s.appKeyStore.Lookup(key)
	if rec == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rec.Snapshot())
}

// handleAppKeyRotate vends a new key inheriting the old key's label (or an
// override) and revokes the old key. Always succeeds for known keys, including
// already-revoked or expired ones (rotation is a recovery operation).
// POST /v1/app-keys/{key}/rotate
// Body (optional): {"label": "..."}
func (s *Server) handleAppKeyRotate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	oldSnap, newSnap, err := s.appKeyStore.Rotate(key, req.Label)
	if err != nil {
		if errors.Is(err, appkey.ErrUnknownKey) {
			s.writeError(w, http.StatusNotFound, fmt.Sprintf("app key %q not found", key))
			return
		}
		s.writeError(w, http.StatusInternalServerError, "failed to rotate key")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Old *appkey.UsageSnapshot `json:"old"`
		New *appkey.UsageSnapshot `json:"new"`
	}{Old: oldSnap, New: newSnap})
}

// handleUsageAggregate returns aggregate usage across all keys.
// GET /v1/usage
func (s *Server) handleUsageAggregate(w http.ResponseWriter, _ *http.Request) {
	snapshots := s.appKeyStore.List()

	type aggregate struct {
		TotalRequests     int64                            `json:"total_requests"`
		StreamRequests    int64                            `json:"stream_requests"`
		NonStreamRequests int64                            `json:"non_stream_requests"`
		Keys              int                              `json:"keys"`
		Models            map[string]*appkey.ModelSnapshot `json:"models,omitempty"`
	}

	agg := &aggregate{
		Models: make(map[string]*appkey.ModelSnapshot),
	}
	agg.Keys = len(snapshots)

	for _, snap := range snapshots {
		agg.TotalRequests += snap.TotalRequests
		agg.StreamRequests += snap.StreamRequests
		agg.NonStreamRequests += snap.NonStreamRequests
		for model, mu := range snap.Models {
			if existing, ok := agg.Models[model]; ok {
				existing.Requests += mu.Requests
				existing.PromptTokens += mu.PromptTokens
				existing.CompletionTokens += mu.CompletionTokens
			} else {
				agg.Models[model] = &appkey.ModelSnapshot{
					Requests:         mu.Requests,
					PromptTokens:     mu.PromptTokens,
					CompletionTokens: mu.CompletionTokens,
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(agg)
}
