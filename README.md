<p align="center">
  <img src="assets/logo.png" alt="Butter logo" width="200">
</p>

<p align="center">
  <img src="assets/meme.png" alt="Butter comic" width="1024">
</p>

<p align="center">
  <a href="https://github.com/Temikus/butter/actions/workflows/ci.yml"><img src="https://github.com/Temikus/butter/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a> <a href="https://goreportcard.com/report/github.com/temikus/butter"><img src="https://goreportcard.com/badge/github.com/temikus/butter" alt="Go Report Card"></a> <img src="https://img.shields.io/github/go-mod/go-version/Temikus/butter" alt="Go Version"> <img src="https://img.shields.io/github/license/Temikus/butter" alt="License">
</p>

A blazingly fast AI proxy gateway written in Go. Butter sits between your application and AI providers, offering a unified OpenAI-compatible API with minimal latency overhead.

Inspired by [Bifrost](https://github.com/maximhq/bifrost), but with a focus on simplicity, extensibility via WASM plugins, and raw performance.

```
Your App ──▶ Butter ──▶ OpenAI / OpenRouter / ...
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
- OpenAI and OpenRouter providers (any OpenAI-compatible API via shared base)
- Multi-provider routing with model-specific provider lists and priority/round-robin strategies
- YAML configuration with environment variable substitution
- Weighted random key selection with per-key model allowlists
- Multi-provider failover with configurable retry-on status codes and exponential backoff
- Raw HTTP passthrough for unsupported endpoints
- Health check endpoint (`/healthz`)
- Graceful shutdown

**Coming soon:**
- More providers (Anthropic, 20+ more)
- Plugin system — built-in Go plugins + sandboxed WASM plugins via [Extism](https://extism.org/)
- Response caching (in-memory LRU, Redis)
- OpenTelemetry tracing and Prometheus metrics

## Quick Start

### Prerequisites

- Go 1.25+ (uses enhanced `ServeMux` pattern routing)
- An API key for a supported provider ([OpenAI](https://platform.openai.com/), [OpenRouter](https://openrouter.ai/), or any OpenAI-compatible API)

### 1. Clone and build

```bash
git clone https://github.com/temikus/butter.git
cd butter
go build -o butter ./cmd/butter/
```

### 2. Configure

```bash
cp config.example.yaml config.yaml
```

Edit `config.yaml` or set environment variables:

```bash
export OPENAI_API_KEY="sk-..."
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
    "gpt-4o-mini":
      providers: [openai, openrouter]
      strategy: priority
  failover:
    enabled: true
    max_retries: 3
    retry_on: [429, 500, 502, 503, 504]
    backoff:
      initial: 100ms
      multiplier: 2.0
      max: 5s
```

</details>

### 3. Run

```bash
./butter -config config.yaml
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
just build              # Build binary
just serve              # Run with config (auto-loads API keys from ~/.openai/api-key, ~/.openrouter/api-key)
just test               # Run all tests with race detector
just lint               # Run golangci-lint
just check              # Run vet + lint + test
just bench              # Run benchmarks with allocation reporting
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
│   └── provider/
│       ├── provider.go          Provider interface & types
│       ├── registry.go          Thread-safe provider registry
│       ├── openaicompat/        Reusable base for OpenAI-compatible APIs
│       ├── openai/              OpenAI provider
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

TBD
