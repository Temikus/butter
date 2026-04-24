# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
just build                            # Build binary (with commit hash)
just build-release                    # Build with full version info from git
just serve                            # Run with config (auto-loads API keys)
just test                             # Run all tests with race detector
just vet                              # Static analysis
just lint                             # Run golangci-lint
just check                            # Run vet + lint + test
just bench                            # Run benchmarks with allocation reporting
just release-snapshot                 # Test GoReleaser locally (no publish)
just test-one ./internal/proxy/ TestDispatch  # Run a single test
just build-example-wasm               # Compile example WASM plugin (requires TinyGo ≥ 0.34)
just build-injection-guard            # Compile prompt injection guard WASM plugin
just build-wasm                       # Build all WASM plugins
```

## Architecture

Butter is an AI proxy gateway that exposes an OpenAI-compatible API and forwards requests to backend AI providers. The request flow is:

```
Client → transport.Server (HTTP) → proxy.Engine (routing/dispatch) → provider.Registry → Provider impl → upstream API
```

**Key packages:**

- `cmd/butter/` — Entrypoint. Wires config, providers, engine, and HTTP server. Sets up graceful shutdown (SIGINT/SIGTERM).
- `internal/config/` — YAML config with `${ENV_VAR}` substitution via regexp. Applies typed defaults for zero-valued fields. `watcher.go` polls mtime and swaps engine state atomically (no restart needed). Per-provider `credential_mode` (`"stored"` default / `"passthrough"`): controls whether the engine injects managed keys or forwards client auth headers. Bedrock-specific fields: `region`, `aws_profile`, `model_map`.
- `internal/transport/` — HTTP server using Go 1.22+ `ServeMux` pattern routing. Handles streaming detection via `bytes.Contains` (no full JSON parse) and SSE relay with per-chunk flush via `http.Flusher`.
- `internal/proxy/` — Engine resolves provider via: explicit `provider` field in request → model-based route from config → default provider. Selects API key and dispatches.
- `internal/provider/` — `Provider` interface (`ChatCompletion`, `ChatCompletionStream`, `Passthrough`, `SupportsOperation`) + optional `EmbeddingProvider` interface + optional `AnthropicNativeHandler` interface (for providers handling Anthropic Messages API format natively) + optional `AuthHeaderSetter` interface + thread-safe `Registry` (RWMutex).
- `internal/provider/openaicompat/` — Shared base for OpenAI-compatible APIs. Line-based SSE parsing with `bufio.Reader`, `sync.Pool` for buffer reuse. Handles `[DONE]` marker.
- `internal/provider/openai/`, `openrouter/`, `groq/`, `mistral/`, `together/`, `fireworks/`, `perplexity/` — Thin wrappers over `openaicompat` with provider-specific base URLs.
- `internal/provider/anthropic/` — Standalone implementation with OpenAI↔Anthropic request/response translation.
- `internal/provider/gemini/` — Standalone implementation with OpenAI↔Gemini request/response translation. Model-in-URL routing, `?key=` auth, streaming via `streamGenerateContent?alt=sse`.
- `internal/provider/bedrock/` — AWS Bedrock provider. Uses `aws-sdk-go-v2` (SigV4 auth); implements `AnthropicNativeHandler` (not standard `ChatCompletion`). Default model map for 8 Claude models, convention fallback, event-stream→SSE streaming conversion. Config: `region`, `aws_profile`, `model_map`.
- `internal/appkey/` — Application key store. Thread-safe in-memory map of `btr_`-prefixed tokens → per-key usage counters (requests, prompt tokens, completion tokens, last_accessed_at). Async token counting via goroutine. Optional bbolt-backed persistence (`persist.go`): write-behind flush on interval + shutdown, immediate write on vend; hot path untouched. Zero overhead when disabled (no middleware, no routes registered).
- `internal/cache/` — Response cache interface with in-memory LRU and Redis backends. Cache key derived from SHA256(provider + model + messages + params). Only caches non-streaming requests with temperature=0. Redis backend uses `go-redis/v9` with key prefixing and native TTL.
- `internal/plugin/` — Plugin interfaces (`TransportPlugin`, `LLMPlugin`, `ObservabilityPlugin`), ordered `Chain`, and `Manager`. Built-in plugins: `ratelimit/`, `requestlog/`, `metrics/` (OTel SDK, Prometheus `/metrics`), `tracing/` (OTel spans, OTLP HTTP export).
- `internal/plugin/wasm/` — WASM plugin host built on Extism/wazero (pure Go, BSD-3/Apache-2.0). Uses `CompiledPlugin` (compile-once at startup) + per-call `Instance()` for safe concurrent use. Missing hooks silently skipped. `StreamChunk` is pass-through (per-chunk instantiation cost is prohibitive).
- `plugin/sdk/` — Public JSON ABI types (`Request`/`Response`) for external WASM plugin authors. Stdlib-only so it compiles with TinyGo.
- `plugins/example-wasm/` — Example TinyGo plugin demonstrating `pre_http`. Build with `just build-example-wasm`.
- `plugins/prompt-injection-guard/` — Prompt injection detection WASM plugin. Scans chat messages for ~60 injection patterns across 7 categories with Unicode bypass detection. Supports block/log/tag modes. Build with `just build-injection-guard`.

**Endpoints:** `POST /v1/chat/completions`, `POST /v1/messages` (Anthropic Messages API — routes to `AnthropicNativeHandler` providers with failover), `POST /v1/embeddings`, `GET /v1/models`, `GET /healthz`, `GET /metrics` (when metrics plugin enabled), `/native/{provider}/*` (raw passthrough). When `app_keys.enabled: true`: `POST /v1/app-keys` (vend key), `GET /v1/app-keys` (list keys), `GET /v1/app-keys/{key}/usage` (per-key stats), `GET /v1/usage` (aggregate stats).

## Design Constraints

- stdlib-only HTTP (no frameworks) — performance target is <50μs proxy overhead
- Direct dependency: `gopkg.in/yaml.v3`; metrics/tracing plugins add OTel SDK + Prometheus; WASM host adds Extism/wazero; Redis cache adds `go-redis/v9`; bbolt persistence adds `go.etcd.io/bbolt`; Bedrock provider adds `aws-sdk-go-v2` (SigV4 auth + bedrockruntime)
- Streaming uses direct byte relay (no JSON re-serialization)
- Go 1.22+ required for pattern-based ServeMux routing
- No HashiCorp licensed dependencies; all deps are Apache-2.0, MIT, BSD, or MPL-2.0

## Phased Roadmap

- **Phase 1** (PoC): complete
- **Phase 2** (Multi-Provider + Routing): complete
- **Phase 3** (Plugin System): complete — Go plugin interfaces + chain + manager + built-in plugins (ratelimit, requestlog, metrics, tracing) + WASM host (Extism/wazero, JSON ABI, plugin SDK, example plugin)
- **Phase 4** (Caching + Observability): complete — in-memory LRU cache, OTel tracing (OTLP HTTP), Prometheus metrics, slog
- **Phase 5** (Production): complete — graceful shutdown, healthz, Docker (distroless), 38 integration tests, config hot-reload, benchmarks
- **Phase 6** (Provider Expansion): complete — Groq, Mistral, Together.ai, Fireworks, Perplexity (all via openaicompat)
- **Phase 7** (Application Keys): complete — `btr_` token vending, per-key usage tracking (requests + prompt/completion tokens + last_accessed_at), optional `require_key` enforcement, management endpoints, 6 integration tests; bbolt write-behind persistence (opt-in, zero hot-path overhead)
- **Phase 8** (API Completeness + Gemini + Redis): complete — `/v1/embeddings` endpoint (optional `EmbeddingProvider` interface, openaicompat support), `/v1/models` endpoint (config-derived model list), Redis cache backend (`go-redis/v9`, key-prefixed, configurable), Google Gemini provider (standalone OpenAI↔Gemini translation, streaming via SSE, `?key=` auth)
- **Phase 9** (Bedrock + Anthropic Native): complete — AWS Bedrock provider (SigV4 via `aws-sdk-go-v2`, `InvokeModel`/`InvokeModelWithResponseStream`, model map + convention fallback), `AnthropicNativeHandler` interface (cross-protocol failover between Anthropic direct and Bedrock), `POST /v1/messages` endpoint (Anthropic Messages API passthrough with routing/failover), per-provider `credential_mode` (`stored`/`passthrough`)
- **Next**: Azure OpenAI, Vertex AI, or semantic cache plugin
