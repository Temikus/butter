package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/temikus/butter/internal/cache"
	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/provider"
)

// engineState holds the hot-reloadable portions of Engine configuration.
// The entire struct is replaced atomically on Reload so in-flight requests
// always see a consistent snapshot.
type engineState struct {
	cfg  *config.Config
	keys map[string][]config.KeyConfig // provider name -> ordered key list
}

func newEngineState(cfg *config.Config) *engineState {
	keys := make(map[string][]config.KeyConfig, len(cfg.Providers))
	for name, p := range cfg.Providers {
		keys[name] = p.Keys
	}
	return &engineState{cfg: cfg, keys: keys}
}

// Engine is the core proxy dispatcher. It resolves which provider to use,
// selects an API key, and dispatches the request.
type Engine struct {
	registry *provider.Registry
	st       atomic.Pointer[engineState] // replaced atomically on Reload
	logger   *slog.Logger
	chain    *plugin.Chain // nil means no plugins
	cache    cache.Cache   // nil means caching disabled
	cacheTTL time.Duration
	// sleepFn is used for backoff delays; overridable for testing.
	sleepFn func(time.Duration)
}

func NewEngine(reg *provider.Registry, cfg *config.Config, logger *slog.Logger, chain *plugin.Chain) *Engine {
	e := &Engine{
		registry: reg,
		logger:   logger,
		chain:    chain,
		sleepFn:  time.Sleep,
	}
	e.st.Store(newEngineState(cfg))
	return e
}

// Reload atomically replaces routing config and key assignments.
// Safe to call while requests are in flight.
func (e *Engine) Reload(cfg *config.Config) {
	e.st.Store(newEngineState(cfg))
}

// SetCache enables response caching with the given cache implementation and TTL.
func (e *Engine) SetCache(c cache.Cache, ttl time.Duration) {
	e.cache = c
	e.cacheTTL = ttl
}

// Dispatch handles a non-streaming chat completion request.
func (e *Engine) Dispatch(ctx context.Context, rawBody []byte) (*provider.ChatResponse, error) {
	st := e.st.Load()
	req, providerNames, err := e.parseAndRoute(st, rawBody)
	if err != nil {
		return nil, err
	}

	// Run LLM pre-hooks.
	// Reuse the RequestContext from transport if available, so provider/model
	// flow back to the transport layer for trace emission.
	pctx := plugin.GetRequestContext(ctx)
	if e.chain != nil {
		if pctx == nil {
			pctx = &plugin.RequestContext{
				Model:     req.Model,
				Body:      rawBody,
				Metadata:  make(map[string]any),
				StartTime: time.Now(),
			}
		} else {
			pctx.Model = req.Model
			pctx.Body = rawBody
		}
		pctx = e.chain.RunPreLLM(pctx)
		rawBody = pctx.Body
		req.Model = pctx.Model
	}

	// Check cache for non-streaming, deterministic requests.
	if e.cache != nil && cache.IsCacheable(rawBody) {
		cacheKey := cache.DeriveKey(providerNames[0], rawBody)
		if cached := e.cache.Get(cacheKey); cached != nil {
			e.logger.Info("cache hit",
				"model", req.Model,
				"provider", providerNames[0],
			)
			if pctx != nil {
				pctx.Provider = providerNames[0]
				pctx.Metadata["cache"] = "hit"
			}
			return &provider.ChatResponse{
				RawBody:    cached,
				StatusCode: 200,
				Headers:    http.Header{"X-Butter-Cache": []string{"hit"}},
			}, nil
		}
	}

	failover := st.cfg.Routing.Failover

	var lastErr error
	for _, providerName := range providerNames {
		p, err := e.registry.Get(providerName)
		if err != nil {
			lastErr = err
			continue
		}

		maxAttempts := 1
		if failover.Enabled {
			maxAttempts = failover.MaxRetries + 1
		}

		for attempt := 0; attempt < maxAttempts; attempt++ {
			if attempt > 0 {
				e.backoff(st, attempt-1)
			}

			req.APIKey = e.selectKey(st, providerName, req.Model)
			req.RawBody = rawBody

			e.logger.Info("dispatching request",
				"provider", providerName,
				"model", req.Model,
				"stream", false,
				"attempt", attempt+1,
			)

			resp, err := p.ChatCompletion(ctx, req)
			if err == nil {
				// Populate provider on shared context for transport trace.
				if pctx != nil {
					pctx.Provider = providerName
				}
				// Run LLM post-hooks.
				if e.chain != nil && pctx != nil {
					pluginResp := &plugin.Response{
						StatusCode: resp.StatusCode,
						Headers:    resp.Headers,
						Body:       resp.RawBody,
					}
					pluginResp = e.chain.RunPostLLM(pctx, pluginResp)
					resp.RawBody = pluginResp.Body
					resp.StatusCode = pluginResp.StatusCode
					resp.Headers = pluginResp.Headers
				}
				// Store in cache if eligible.
				if e.cache != nil && resp.StatusCode == 200 && cache.IsCacheable(rawBody) {
					cacheKey := cache.DeriveKey(providerName, rawBody)
					e.cache.Set(cacheKey, resp.RawBody, e.cacheTTL)
				}
				return resp, nil
			}

			lastErr = err

			if !failover.Enabled {
				return nil, err
			}

			var pe *provider.ProviderError
			if errors.As(err, &pe) && e.isRetryable(st, pe.StatusCode) {
				continue // retry same provider
			}
			break // non-retryable, try next provider
		}
	}

	return nil, lastErr
}

// DispatchStream handles a streaming chat completion request.
func (e *Engine) DispatchStream(ctx context.Context, rawBody []byte) (provider.Stream, error) {
	st := e.st.Load()
	req, providerNames, err := e.parseAndRoute(st, rawBody)
	if err != nil {
		return nil, err
	}

	// Run LLM pre-hooks.
	// Reuse the RequestContext from transport if available.
	pctx := plugin.GetRequestContext(ctx)
	if e.chain != nil {
		if pctx == nil {
			pctx = &plugin.RequestContext{
				Model:     req.Model,
				Body:      rawBody,
				Metadata:  make(map[string]any),
				StartTime: time.Now(),
			}
		} else {
			pctx.Model = req.Model
			pctx.Body = rawBody
		}
		pctx = e.chain.RunPreLLM(pctx)
		rawBody = pctx.Body
		req.Model = pctx.Model
	}

	failover := st.cfg.Routing.Failover

	var lastErr error
	for _, providerName := range providerNames {
		p, err := e.registry.Get(providerName)
		if err != nil {
			lastErr = err
			continue
		}

		maxAttempts := 1
		if failover.Enabled {
			maxAttempts = failover.MaxRetries + 1
		}

		for attempt := 0; attempt < maxAttempts; attempt++ {
			if attempt > 0 {
				e.backoff(st, attempt-1)
			}

			req.APIKey = e.selectKey(st, providerName, req.Model)
			req.RawBody = rawBody

			e.logger.Info("dispatching stream request",
				"provider", providerName,
				"model", req.Model,
				"stream", true,
				"attempt", attempt+1,
			)

			stream, err := p.ChatCompletionStream(ctx, req)
			if err == nil {
				// Populate provider on shared context for transport trace.
				if pctx != nil {
					pctx.Provider = providerName
				}
				return stream, nil
			}

			lastErr = err

			if !failover.Enabled {
				return nil, err
			}

			var pe *provider.ProviderError
			if errors.As(err, &pe) && e.isRetryable(st, pe.StatusCode) {
				continue // retry same provider
			}
			break // non-retryable, try next provider
		}
	}

	return nil, lastErr
}

// parseAndRoute extracts the model from the request body and determines
// which provider(s) should handle it. Returns an ordered list of providers
// to try (for failover).
func (e *Engine) parseAndRoute(st *engineState, rawBody []byte) (*provider.ChatRequest, []string, error) {
	// Minimal parse — only extract fields needed for routing.
	var partial struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Provider string `json:"provider"` // Optional explicit provider override
	}
	if err := json.Unmarshal(rawBody, &partial); err != nil {
		return nil, nil, fmt.Errorf("invalid request body: %w", err)
	}

	if partial.Model == "" {
		return nil, nil, fmt.Errorf("missing required field: model")
	}

	var providerNames []string

	if partial.Provider != "" {
		// Explicit override — single provider, no failover chain.
		providerNames = []string{partial.Provider}
	} else if route, ok := st.cfg.Routing.Models[partial.Model]; ok && len(route.Providers) > 0 {
		// Model route — full provider list for failover.
		providerNames = route.Providers
	} else if st.cfg.Routing.DefaultProvider != "" {
		providerNames = []string{st.cfg.Routing.DefaultProvider}
	}

	if len(providerNames) == 0 {
		return nil, nil, fmt.Errorf("no provider configured for model %q", partial.Model)
	}

	req := &provider.ChatRequest{
		Model:   partial.Model,
		Stream:  partial.Stream,
		RawBody: rawBody,
	}

	return req, providerNames, nil
}

// selectKey picks an API key for the given provider using weighted random selection.
// Keys with a Models allowlist are skipped if the requested model isn't in the list.
func (e *Engine) selectKey(st *engineState, providerName, model string) string {
	keys := st.keys[providerName]
	if len(keys) == 0 {
		return ""
	}

	// Filter keys by model allowlist and compute total weight.
	totalWeight := 0
	eligible := make([]config.KeyConfig, 0, len(keys))
	for _, k := range keys {
		if len(k.Models) > 0 && !containsString(k.Models, model) {
			continue
		}
		eligible = append(eligible, k)
		totalWeight += k.Weight
	}

	if len(eligible) == 0 {
		return ""
	}
	if len(eligible) == 1 {
		return eligible[0].Key
	}

	// Weighted random selection.
	r := rand.IntN(totalWeight)
	for _, k := range eligible {
		r -= k.Weight
		if r < 0 {
			return k.Key
		}
	}

	// Fallback (shouldn't reach here).
	return eligible[0].Key
}

// isRetryable checks if a status code is in the configured retry_on list.
func (e *Engine) isRetryable(st *engineState, statusCode int) bool {
	for _, code := range st.cfg.Routing.Failover.RetryOn {
		if code == statusCode {
			return true
		}
	}
	return false
}

// backoff sleeps for the computed backoff duration: min(initial * multiplier^attempt, max).
func (e *Engine) backoff(st *engineState, attempt int) {
	bo := st.cfg.Routing.Failover.Backoff
	delay := time.Duration(float64(bo.Initial) * pow(bo.Multiplier, attempt))
	if delay > bo.Max {
		delay = bo.Max
	}
	e.sleepFn(delay)
}

// pow computes base^exp for small integer exponents.
func pow(base float64, exp int) float64 {
	result := 1.0
	for i := 0; i < exp; i++ {
		result *= base
	}
	return result
}

// DispatchPassthrough forwards a raw HTTP request to the named provider.
// It selects an API key and delegates to the provider's Passthrough method.
func (e *Engine) DispatchPassthrough(ctx context.Context, providerName, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	st := e.st.Load()
	p, err := e.registry.Get(providerName)
	if err != nil {
		return nil, err
	}

	if !p.SupportsOperation(provider.OpPassthrough) {
		return nil, fmt.Errorf("provider %q does not support passthrough", providerName)
	}

	// Inject stored credentials unless the provider is configured for passthrough,
	// in which case the client's own auth headers are forwarded unchanged.
	provCfg := st.cfg.Providers[providerName]
	if provCfg.CredentialMode != "passthrough" {
		apiKey := e.selectKey(st, providerName, "")
		if apiKey != "" {
			headers = headers.Clone()
			if setter, ok := p.(provider.AuthHeaderSetter); ok {
				setter.SetAuthHeader(headers, apiKey)
			} else {
				headers.Set("Authorization", "Bearer "+apiKey)
			}
		}
	}

	e.logger.Info("dispatching passthrough",
		"provider", providerName,
		"method", method,
		"path", path,
	)

	return p.Passthrough(ctx, method, path, body, headers)
}

// DispatchEmbeddings routes an embedding request to a capable provider.
func (e *Engine) DispatchEmbeddings(ctx context.Context, rawBody []byte) (*provider.EmbeddingResponse, error) {
	st := e.st.Load()

	// Minimal parse — only extract model and optional provider.
	var partial struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(rawBody, &partial); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	if partial.Model == "" {
		return nil, fmt.Errorf("missing required field: model")
	}

	providerNames := e.resolveProviders(st, partial.Model, partial.Provider)
	if len(providerNames) == 0 {
		return nil, fmt.Errorf("no provider configured for model %q", partial.Model)
	}

	failover := st.cfg.Routing.Failover
	var lastErr error

	for _, name := range providerNames {
		p, err := e.registry.Get(name)
		if err != nil {
			lastErr = err
			continue
		}

		ep, ok := p.(provider.EmbeddingProvider)
		if !ok {
			lastErr = fmt.Errorf("provider %q does not support embeddings", name)
			continue
		}

		maxAttempts := 1
		if failover.Enabled {
			maxAttempts = failover.MaxRetries + 1
		}

		for attempt := range maxAttempts {
			if attempt > 0 {
				e.backoff(st, attempt-1)
			}

			apiKey := e.selectKey(st, name, partial.Model)
			req := &provider.EmbeddingRequest{
				Model:   partial.Model,
				RawBody: rawBody,
				APIKey:  apiKey,
			}

			e.logger.Info("dispatching embeddings", "provider", name, "model", partial.Model)
			resp, err := ep.Embeddings(ctx, req)
			if err != nil {
				lastErr = err
				var pe *provider.ProviderError
				if errors.As(err, &pe) && e.isRetryable(st, pe.StatusCode) {
					continue
				}
				break
			}
			return resp, nil
		}
	}

	return nil, lastErr
}

// ListModels returns the list of models derived from the routing configuration.
func (e *Engine) ListModels() *provider.ModelListResponse {
	st := e.st.Load()
	models := make([]provider.ModelInfo, 0, len(st.cfg.Routing.Models))

	for modelID, route := range st.cfg.Routing.Models {
		ownedBy := "butter"
		if len(route.Providers) > 0 {
			ownedBy = route.Providers[0]
		}
		models = append(models, provider.ModelInfo{
			ID:      modelID,
			Object:  "model",
			Created: 0,
			OwnedBy: ownedBy,
		})
	}

	return &provider.ModelListResponse{
		Object: "list",
		Data:   models,
	}
}

// resolveProviders determines the ordered list of providers for a given model.
func (e *Engine) resolveProviders(st *engineState, model, explicitProvider string) []string {
	if explicitProvider != "" {
		return []string{explicitProvider}
	}
	if route, ok := st.cfg.Routing.Models[model]; ok && len(route.Providers) > 0 {
		return route.Providers
	}
	if st.cfg.Routing.DefaultProvider != "" {
		return []string{st.cfg.Routing.DefaultProvider}
	}
	return nil
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
