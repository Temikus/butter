<p align="center">
  <img src="assets/logo.jpg" alt="Butter logo" width="200">
</p>

<p align="center">
  <img src="assets/comic.jpg" alt="Butter comic" width="1024">
</p>

<p align="center">
  <a href="https://github.com/Temikus/butter/releases"><img src="https://img.shields.io/github/v/release/Temikus/butter" alt="Release"></a> <a href="https://github.com/Temikus/butter/actions/workflows/ci.yml"><img src="https://github.com/Temikus/butter/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a> <a href="https://goreportcard.com/report/github.com/temikus/butter"><img src="https://goreportcard.com/badge/github.com/temikus/butter" alt="Go Report Card"></a> <img src="https://img.shields.io/github/go-mod/go-version/Temikus/butter" alt="Go Version"> <img src="https://img.shields.io/github/license/Temikus/butter" alt="License">
</p>

A blazingly fast AI proxy gateway written in Go. Butter sits between your application and AI providers, offering a unified OpenAI-compatible API with minimal latency overhead.

Inspired by [Bifrost](https://github.com/maximhq/bifrost), but with a focus on simplicity, extensibility via WASM plugins, and raw performance.

```
Your App ──▶ Butter ──▶ OpenAI / Anthropic / OpenRouter / ...
                │
                ├── Unified OpenAI-compatible API
                ├── Automatic failover & retries
                ├── Weighted key rotation
                └── Plugin hooks (Go + WASM)
```

## Features

**Available now:**
- OpenAI-compatible `/v1/chat/completions` endpoint
- Streaming (SSE) and non-streaming responses
- OpenAI, Anthropic, and OpenRouter providers (any OpenAI-compatible API via shared base)
- Anthropic format translation (OpenAI requests automatically converted to/from Anthropic's native format)
- Multi-provider routing with model-specific provider lists and priority/round-robin strategies
- YAML configuration with environment variable substitution
- Weighted random key selection with per-key model allowlists
- Multi-provider failover with configurable retry-on status codes and exponential backoff
- Plugin system with ordered hook chains (pre/post HTTP, pre/post LLM, stream chunks, observability traces)
- Built-in request logging plugin (structured slog traces with provider, model, status, duration)
- Built-in rate limiter plugin (token bucket, global or per-IP, configurable RPM)
- Plugin short-circuit support (plugins can reject requests before they reach the provider)
- Raw HTTP passthrough for unsupported endpoints (`/native/{provider}/*`)
- Health check endpoint (`/healthz`)
- Graceful shutdown

**Coming soon:**
- More providers (20+ to match Bifrost coverage)
- WASM plugin sandbox via [Extism](https://extism.org/) for external plugins
- Built-in Prometheus metrics plugin
- Response caching (in-memory LRU, Redis)
- OpenTelemetry tracing

## Quick Start

### Prerequisites

- Go 1.25+ (uses enhanced `ServeMux` pattern routing)
- An API key for a supported provider ([OpenAI](https://platform.openai.com/), [Anthropic](https://console.anthropic.com/), [OpenRouter](https://openrouter.ai/), or any OpenAI-compatible API)

### 1. Install

Download the latest binary from [GitHub Releases](https://github.com/temikus/butter/releases), or build from source:

```bash
git clone https://github.com/temikus/butter.git
cd butter
go build -o pkg/bin/butter ./cmd/butter/
```

### 2. Configure

```bash
cp config.example.yaml config.yaml
```

Edit `config.yaml` or set environment variables:

```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export OPENROUTER_API_KEY="sk-or-v1-..."
```

The config file supports `${ENV_VAR}` substitution, so the default `config.example.yaml` works out of the box once the environment variables are set.

<details>
<summary>Example config.yaml</summary>

```yaml
server:
  address: ":8080"
  read_timeout: 30s
  write_timeout: 120s

providers:
  openai:
    base_url: https://api.openai.com/v1
    keys:
      - key: "${OPENAI_API_KEY}"
        weight: 1

  anthropic:
    base_url: https://api.anthropic.com/v1
    keys:
      - key: "${ANTHROPIC_API_KEY}"
        weight: 1

  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "${OPENROUTER_API_KEY}"
        weight: 1

routing:
  default_provider: openrouter
  models:
    "gpt-4o":
      providers: [openai, openrouter]
      strategy: priority
    "claude-sonnet-4-20250514":
      providers: [anthropic, openrouter]
      strategy: priority
  failover:
    enabled: true
    max_retries: 3
    retry_on: [429, 500, 502, 503, 504]
    backoff:
      initial: 100ms
      multiplier: 2.0
      max: 5s

plugins:
  ratelimit:
    requests_per_minute: 60
    per_ip: false
  requestlog:
    level: info
```

</details>

### 3. Run

```bash
./pkg/bin/butter -config config.yaml
```

You should see:

```json
{"level":"INFO","msg":"butter listening","address":":8080"}
```

### 4. Send a request

**Non-streaming:**

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Say hello in three languages"}]
  }'
```

**Streaming:**

```bash
curl http://localhost:8080/v1/chat/completions \
  --no-buffer \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Count to 5"}],
    "stream": true
  }'
```

**Health check:**

```bash
curl http://localhost:8080/healthz
# ok
```

### Drop-in replacement

Butter is compatible with any OpenAI SDK client. Just point the base URL at your Butter instance:

**Python (openai SDK):**

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="unused",  # Butter uses its own configured keys
)

response = client.chat.completions.create(
    model="openai/gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(response.choices[0].message.content)
```

**Node.js (openai SDK):**

```javascript
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "http://localhost:8080/v1",
  apiKey: "unused",
});

const completion = await client.chat.completions.create({
  model: "openai/gpt-4o-mini",
  messages: [{ role: "user", content: "Hello!" }],
});
console.log(completion.choices[0].message.content);
```

## Development

A [`justfile`](https://github.com/casey/just) is provided for common tasks:

```bash
just build              # Build binary (with commit hash)
just build-release      # Build with full version info from git
just serve              # Run with config (auto-loads API keys from ~/.openai/api-key, ~/.openrouter/api-key)
just test               # Run all tests with race detector
just lint               # Run golangci-lint
just check              # Run vet + lint + test
just bench              # Run benchmarks with allocation reporting
just release-snapshot   # Test GoReleaser locally (no publish)
```

Or use Go directly:

```bash
go run ./cmd/butter/ -config config.yaml
go test ./... -v -race -count=1
go test ./... -bench=. -benchmem
```

### Project structure

```
butter/
├── cmd/butter/                  Main binary
├── internal/
│   ├── config/                  YAML config with env var substitution
│   ├── transport/               HTTP server and handlers
│   ├── proxy/                   Core dispatch engine (routing, failover, key selection)
│   ├── plugin/                  Plugin system (interfaces, chain, manager)
│   │   └── builtin/
│   │       ├── ratelimit/       Token bucket rate limiter plugin
│   │       └── requestlog/      Request logging plugin
│   └── provider/
│       ├── provider.go          Provider interface & types
│       ├── registry.go          Thread-safe provider registry
│       ├── openaicompat/        Reusable base for OpenAI-compatible APIs
│       ├── openai/              OpenAI provider
│       ├── anthropic/           Anthropic provider (format translation)
│       └── openrouter/          OpenRouter provider
├── config.example.yaml
├── justfile
└── go.mod                       (single dependency: gopkg.in/yaml.v3)
```

## Performance Targets

| Metric | Target |
|--------|--------|
| Per-request overhead (no plugins) | <50us |
| Per-request overhead (built-in plugins) | <100us |
| Per-request overhead (1 WASM plugin) | <150us |
| Streaming TTFB overhead | <1ms |
| Memory at idle | <30MB |

## License

Apache 2.0 License. See [LICENSE](LICENSE) for details.
