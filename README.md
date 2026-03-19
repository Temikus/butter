<p align="center">
  <img src="assets/logo.png" alt="Butter logo" width="200">
</p>

<p align="center">
  <img src="assets/meme.png" alt="Butter comic" width="1024">
</p>

A blazingly fast AI proxy gateway written in Go. Butter sits between your application and AI providers, offering a unified OpenAI-compatible API with minimal latency overhead.

Inspired by [Bifrost](https://github.com/maximhq/bifrost), but with a focus on simplicity, extensibility via WASM plugins, and raw performance.

```
Your App ──▶ Butter ──▶ OpenRouter / OpenAI / Anthropic / ...
                │
                ├── Unified OpenAI-compatible API
                ├── Automatic failover & retries
                ├── Weighted key rotation
                └── Plugin hooks (Go + WASM)
```

## Features

**Available now (Phase 1):**
- OpenAI-compatible `/v1/chat/completions` endpoint
- Streaming (SSE) and non-streaming responses
- OpenRouter provider with full passthrough
- YAML configuration with environment variable substitution
- Health check endpoint (`/healthz`)
- Graceful shutdown

**Coming soon:**
- Multi-provider routing (OpenAI, Anthropic, 20+ more)
- Weighted load balancing and failover with exponential backoff
- Plugin system — built-in Go plugins + sandboxed WASM plugins via [Extism](https://extism.org/)
- Response caching (in-memory LRU, Redis)
- OpenTelemetry tracing and Prometheus metrics

## Quick Start

### Prerequisites

- Go 1.22+ (uses enhanced `ServeMux` pattern routing)
- An [OpenRouter](https://openrouter.ai/) API key (or any OpenAI-compatible provider)

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
export OPENROUTER_API_KEY="sk-or-v1-your-key-here"
```

The config file supports `${ENV_VAR}` substitution, so the default `config.example.yaml` works out of the box once the environment variable is set.

<details>
<summary>Example config.yaml</summary>

```yaml
server:
  address: ":8080"
  read_timeout: 30s
  write_timeout: 120s

providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "${OPENROUTER_API_KEY}"
        weight: 1

routing:
  default_provider: openrouter
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

### Run from source

```bash
go run ./cmd/butter/ -config config.yaml
```

### Run tests

```bash
go test ./... -v -race -count=1   # Run all tests with race detector
go test ./... -bench=. -benchmem  # Run benchmarks
```

### Project structure

```
butter/
├── cmd/butter/              Main binary
├── internal/
│   ├── config/              YAML config with env var substitution
│   ├── transport/           HTTP server and handlers
│   ├── proxy/               Core dispatch engine
│   └── provider/
│       ├── provider.go      Provider interface
│       ├── registry.go      Provider registry
│       └── openrouter/      OpenRouter implementation
├── config.example.yaml
└── go.mod
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
