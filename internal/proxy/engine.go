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
	"time"

	"github.com/temikus/butter/internal/config"
	"github.com/temikus/butter/internal/plugin"
	"github.com/temikus/butter/internal/provider"
)

// Engine is the core proxy dispatcher. It resolves which provider to use,
// selects an API key, and dispatches the request.
type Engine struct {
	registry *provider.Registry
	config   *config.Config
	keys     map[string][]config.KeyConfig // provider name -> keys
	logger   *slog.Logger
	chain    *plugin.Chain // nil means no plugins
	// sleepFn is used for backoff delays; overridable for testing.
	sleepFn func(time.Duration)
}

func NewEngine(reg *provider.Registry, cfg *config.Config, logger *slog.Logger, chain *plugin.Chain) *Engine {
	keys := make(map[string][]config.KeyConfig)
	for name, p := range cfg.Providers {
		keys[name] = p.Keys
	}
	return &Engine{
		registry: reg,
		config:   cfg,
		keys:     keys,
		logger:   logger,
		chain:    chain,
		sleepFn:  time.Sleep,
	}
}

// Dispatch handles a non-streaming chat completion request.
func (e *Engine) Dispatch(ctx context.Context, rawBody []byte) (*provider.ChatResponse, error) {
	req, providerNames, err := e.parseAndRoute(rawBody)
	if err != nil {
		return nil, err
	}

	// Run LLM pre-hooks.
	var pctx *plugin.RequestContext
	if e.chain != nil {
		pctx = &plugin.RequestContext{
			Model:     req.Model,
			Body:      rawBody,
			Metadata:  make(map[string]any),
			StartTime: time.Now(),
		}
		pctx = e.chain.RunPreLLM(pctx)
		rawBody = pctx.Body
		req.Model = pctx.Model
	}

	failover := e.config.Routing.Failover

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
				e.backoff(attempt - 1)
			}

			req.APIKey = e.selectKey(providerName, req.Model)
			req.RawBody = rawBody

			e.logger.Info("dispatching request",
				"provider", providerName,
				"model", req.Model,
				"stream", false,
				"attempt", attempt+1,
			)

			resp, err := p.ChatCompletion(ctx, req)
			if err == nil {
				// Run LLM post-hooks.
				if e.chain != nil && pctx != nil {
					pctx.Provider = providerName
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
				return resp, nil
			}

			lastErr = err

			if !failover.Enabled {
				return nil, err
			}

			var pe *provider.ProviderError
			if errors.As(err, &pe) && e.isRetryable(pe.StatusCode) {
				continue // retry same provider
			}
			break // non-retryable, try next provider
		}
	}

	return nil, lastErr
}

// DispatchStream handles a streaming chat completion request.
func (e *Engine) DispatchStream(ctx context.Context, rawBody []byte) (provider.Stream, error) {
	req, providerNames, err := e.parseAndRoute(rawBody)
	if err != nil {
		return nil, err
	}

	// Run LLM pre-hooks.
	if e.chain != nil {
		pctx := &plugin.RequestContext{
			Model:     req.Model,
			Body:      rawBody,
			Metadata:  make(map[string]any),
			StartTime: time.Now(),
		}
		pctx = e.chain.RunPreLLM(pctx)
		rawBody = pctx.Body
		req.Model = pctx.Model
	}

	failover := e.config.Routing.Failover

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
				e.backoff(attempt - 1)
			}

			req.APIKey = e.selectKey(providerName, req.Model)
			req.RawBody = rawBody

			e.logger.Info("dispatching stream request",
				"provider", providerName,
				"model", req.Model,
				"stream", true,
				"attempt", attempt+1,
			)

			stream, err := p.ChatCompletionStream(ctx, req)
			if err == nil {
				return stream, nil
			}

			lastErr = err

			if !failover.Enabled {
				return nil, err
			}

			var pe *provider.ProviderError
			if errors.As(err, &pe) && e.isRetryable(pe.StatusCode) {
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
func (e *Engine) parseAndRoute(rawBody []byte) (*provider.ChatRequest, []string, error) {
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
	} else if route, ok := e.config.Routing.Models[partial.Model]; ok && len(route.Providers) > 0 {
		// Model route — full provider list for failover.
		providerNames = route.Providers
	} else if e.config.Routing.DefaultProvider != "" {
		providerNames = []string{e.config.Routing.DefaultProvider}
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
func (e *Engine) selectKey(providerName, model string) string {
	keys := e.keys[providerName]
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
func (e *Engine) isRetryable(statusCode int) bool {
	for _, code := range e.config.Routing.Failover.RetryOn {
		if code == statusCode {
			return true
		}
	}
	return false
}

// backoff sleeps for the computed backoff duration: min(initial * multiplier^attempt, max).
func (e *Engine) backoff(attempt int) {
	bo := e.config.Routing.Failover.Backoff
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
	p, err := e.registry.Get(providerName)
	if err != nil {
		return nil, err
	}

	if !p.SupportsOperation(provider.OpPassthrough) {
		return nil, fmt.Errorf("provider %q does not support passthrough", providerName)
	}

	// Select a key (model-agnostic for passthrough).
	apiKey := e.selectKey(providerName, "")
	if apiKey != "" {
		headers = headers.Clone()
		if setter, ok := p.(provider.AuthHeaderSetter); ok {
			setter.SetAuthHeader(headers, apiKey)
		} else {
			headers.Set("Authorization", "Bearer "+apiKey)
		}
	}

	e.logger.Info("dispatching passthrough",
		"provider", providerName,
		"method", method,
		"path", path,
	)

	return p.Passthrough(ctx, method, path, body, headers)
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
