# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
go build ./...                        # Build all packages
go build -o butter ./cmd/butter/      # Build binary
go run ./cmd/butter/ -config config.yaml  # Run from source
go test ./... -v -race -count=1       # Run all tests (matches CI)
go test ./internal/proxy/ -run TestDispatch -v  # Run a single test
go vet ./...                          # Static analysis
golangci-lint run                     # Lint (CI uses golangci-lint-action v6)
go test ./... -bench=. -benchmem     # Run benchmarks with allocation reporting
```

## Architecture

Butter is an AI proxy gateway that exposes an OpenAI-compatible API and forwards requests to backend AI providers. The request flow is:

```
Client → transport.Server (HTTP) → proxy.Engine (routing/dispatch) → provider.Registry → Provider impl → upstream API
```

**Key packages:**

- `cmd/butter/` — Entrypoint. Wires config, providers, engine, and HTTP server. Sets up graceful shutdown (SIGINT/SIGTERM).
- `internal/config/` — YAML config with `${ENV_VAR}` substitution via regexp. Applies typed defaults for zero-valued fields.
- `internal/transport/` — HTTP server using Go 1.22+ `ServeMux` pattern routing. Handles streaming detection via `bytes.Contains` (no full JSON parse) and SSE relay with per-chunk flush via `http.Flusher`.
- `internal/proxy/` — Engine resolves provider via: explicit `provider` field in request → model-based route from config → default provider. Selects API key and dispatches.
- `internal/provider/` — `Provider` interface (`ChatCompletion`, `ChatCompletionStream`, `Passthrough`, `SupportsOperation`) + thread-safe `Registry` (RWMutex).
- `internal/provider/openrouter/` — OpenRouter implementation. Line-based SSE parsing with `bufio.Reader`, `sync.Pool` for buffer reuse. Handles `[DONE]` marker.

**Endpoints:** `POST /v1/chat/completions`, `GET /healthz`

## Design Constraints

- stdlib-only HTTP (no frameworks) — performance target is <50μs proxy overhead
- Single external dependency: `gopkg.in/yaml.v3`
- Streaming uses direct byte relay (no JSON re-serialization)
- Go 1.22+ required for pattern-based ServeMux routing
- No HashiCorp licensed dependencies

## Phased Roadmap

Phase 1 (PoC) is complete. Phases 2-5 add multi-provider routing, WASM plugin system (Extism/wazero), caching, and observability.
