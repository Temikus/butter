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
```

## Architecture

Butter is an AI proxy gateway that exposes an OpenAI-compatible API and forwards requests to backend AI providers. The request flow is:

```
Client → transport.Server (HTTP) → proxy.Engine (routing/dispatch) → provider.Registry → Provider impl → upstream API
```

**Key packages:**

- `cmd/butter/` — Entrypoint. Wires config, providers, engine, and HTTP server. Sets up graceful shutdown (SIGINT/SIGTERM).
- `internal/config/` — YAML config with `${ENV_VAR}` substitution via regexp. Applies typed defaults for zero-valued fields. `watcher.go` polls mtime and swaps engine state atomically (no restart needed).
- `internal/transport/` — HTTP server using Go 1.22+ `ServeMux` pattern routing. Handles streaming detection via `bytes.Contains` (no full JSON parse) and SSE relay with per-chunk flush via `http.Flusher`.
- `internal/proxy/` — Engine resolves provider via: explicit `provider` field in request → model-based route from config → default provider. Selects API key and dispatches.
- `internal/provider/` — `Provider` interface (`ChatCompletion`, `ChatCompletionStream`, `Passthrough`, `SupportsOperation`) + thread-safe `Registry` (RWMutex).
- `internal/provider/openrouter/` — OpenRouter implementation. Line-based SSE parsing with `bufio.Reader`, `sync.Pool` for buffer reuse. Handles `[DONE]` marker.
- `internal/cache/` — Response cache interface + in-memory LRU with TTL. Cache key derived from SHA256(provider + model + messages + params). Only caches non-streaming requests with temperature=0.
- `internal/plugin/` — Plugin interfaces (`TransportPlugin`, `LLMPlugin`, `ObservabilityPlugin`), ordered `Chain`, and `Manager`. Built-in plugins: `ratelimit/`, `requestlog/`, `metrics/` (OTel SDK, Prometheus `/metrics`), `tracing/` (OTel spans, OTLP HTTP export).
- `internal/plugin/wasm/` — WASM plugin host built on Extism/wazero (pure Go, BSD-3/Apache-2.0). Uses `CompiledPlugin` (compile-once at startup) + per-call `Instance()` for safe concurrent use. Missing hooks silently skipped. `StreamChunk` is pass-through (per-chunk instantiation cost is prohibitive).
- `plugin/sdk/` — Public JSON ABI types (`Request`/`Response`) for external WASM plugin authors. Stdlib-only so it compiles with TinyGo.
- `plugins/example-wasm/` — Example TinyGo plugin demonstrating `pre_http`. Build with `just build-example-wasm`.

**Endpoints:** `POST /v1/chat/completions`, `GET /healthz`, `GET /metrics` (when metrics plugin enabled), `/native/{provider}/*` (raw passthrough)

## Design Constraints

- stdlib-only HTTP (no frameworks) — performance target is <50μs proxy overhead
- Direct dependency: `gopkg.in/yaml.v3`; metrics/tracing plugins add OTel SDK + Prometheus; WASM host adds Extism/wazero
- Streaming uses direct byte relay (no JSON re-serialization)
- Go 1.22+ required for pattern-based ServeMux routing
- No HashiCorp licensed dependencies; all deps are Apache-2.0, MIT, BSD, or MPL-2.0

## Phased Roadmap

Phases 1–5 are complete. Phase 3 WASM is now also complete.

- **Phase 1** (PoC): complete
- **Phase 2** (Multi-Provider + Routing): complete
- **Phase 3** (Plugin System): complete — Go plugin interfaces + chain + manager + built-in plugins (ratelimit, requestlog, metrics, tracing) + WASM host (Extism/wazero, JSON ABI, plugin SDK, example plugin)
- **Phase 4** (Caching + Observability): complete — in-memory LRU cache, OTel tracing (OTLP HTTP), Prometheus metrics, slog
- **Phase 5** (Production): complete — graceful shutdown, healthz, Docker (distroless), 22 integration tests, config hot-reload, benchmarks
- **Next**: provider expansion (Azure, Bedrock, Gemini, Groq, …) or Redis cache backend
