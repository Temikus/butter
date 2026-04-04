# Default recipe
default: build

# Build the binary (with commit hash)
build:
    go build -ldflags "-X github.com/temikus/butter/internal/version.Commit=$(git rev-parse --short HEAD)" \
      -o pkg/bin/butter ./cmd/butter/

# Build with full version info from git
build-release:
    go build -ldflags "-s -w \
      -X github.com/temikus/butter/internal/version.Version=$(git describe --tags --always --dirty) \
      -X github.com/temikus/butter/internal/version.Commit=$(git rev-parse --short HEAD) \
      -X github.com/temikus/butter/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o pkg/bin/butter ./cmd/butter/

# Test release locally (no publish)
release-snapshot:
    goreleaser release --snapshot --clean

# Run from source with config (default: config.example.yaml)
serve config="config.example.yaml":
    #!/usr/bin/env bash
    set -euo pipefail

    # Try to load API keys from files if env vars are not set
    if [ -z "${OPENROUTER_API_KEY:-}" ] && [ -f "$HOME/.openrouter/api-key" ]; then
        export OPENROUTER_API_KEY="$(cat "$HOME/.openrouter/api-key")"
        echo "Loaded OPENROUTER_API_KEY from ~/.openrouter/api-key"
    fi
    if [ -z "${OPENAI_API_KEY:-}" ] && [ -f "$HOME/.openai/api-key" ]; then
        export OPENAI_API_KEY="$(cat "$HOME/.openai/api-key")"
        echo "Loaded OPENAI_API_KEY from ~/.openai/api-key"
    fi

    # Bail if no keys found at all
    if [ -z "${OPENROUTER_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
        echo "Error: No API keys found." >&2
        echo "" >&2
        echo "Set at least one of:" >&2
        echo "  export OPENROUTER_API_KEY=sk-..." >&2
        echo "  export OPENAI_API_KEY=sk-..." >&2
        echo "" >&2
        echo "Or create a key file:" >&2
        echo "  mkdir -p ~/.openrouter && echo 'sk-...' > ~/.openrouter/api-key" >&2
        echo "  mkdir -p ~/.openai && echo 'sk-...' > ~/.openai/api-key" >&2
        exit 1
    fi

    go run ./cmd/butter/ -config {{config}}

# Run all tests, or a subcommand: integration, one <pkg> <name>
#   just test                                  — all tests (unit + integration)
#   just test integration                      — integration tests only
#   just test one ./internal/proxy/ TestDispatch — single test
test *args:
    #!/usr/bin/env bash
    set -euo pipefail
    set -- {{args}}
    subcommand="${1:-}"
    case "$subcommand" in
      "")
        go test ./... -v -race -count=1
        go test ./tests/integration/... -v -race -count=1 -tags integration
        ;;
      integration)
        go test ./tests/integration/... -v -race -count=1 -tags integration
        ;;
      one)
        pkg="${2:?Usage: just test one <pkg> <name>}"
        name="${3:?Usage: just test one <pkg> <name>}"
        go test "$pkg" -run "$name" -v -race -count=1
        ;;
      *)
        echo "Unknown subcommand: $subcommand" >&2
        exit 1
        ;;
    esac

# Lint
lint:
    golangci-lint run

# Static analysis
vet:
    go vet ./...

# Run all checks (vet + lint + all tests)
check: vet lint test

# Build Docker image: just docker-build [tag]
docker-build tag="latest":
    docker build \
      --build-arg VERSION=$(git describe --tags --always --dirty) \
      --build-arg COMMIT=$(git rev-parse --short HEAD) \
      --build-arg DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
      -t butter:{{tag}} .

# Run Docker container: just docker-run [tag] [config]
docker-run tag="latest" config="config.yaml":
    docker run --rm -p 8080:8080 \
      -v "$(pwd)/{{config}}:/config.yaml:ro" \
      butter:{{tag}}

# Benchmarks with allocation reporting
bench:
    go test ./... -bench=. -benchmem

# Build example WASM plugin (requires TinyGo)
build-example-wasm:
    tinygo build -o plugins/example-wasm/example-wasm.wasm \
      -scheduler=none -target=wasi \
      ./plugins/example-wasm/

# Build prompt-injection-guard WASM plugin (requires TinyGo >= 0.34)
build-injection-guard:
    tinygo build -o plugins/prompt-injection-guard/prompt-injection-guard.wasm \
      -scheduler=none -target=wasi \
      ./plugins/prompt-injection-guard/

# Build all WASM plugins
build-wasm: build-example-wasm build-injection-guard

# Remove built binary and compiled WASM plugins
clean:
    rm -rf pkg/
    rm -f plugins/example-wasm/example-wasm.wasm
    rm -f plugins/prompt-injection-guard/prompt-injection-guard.wasm

# Tag and push a release: just release [patch|minor|major]
release bump="patch":
    #!/usr/bin/env bash
    set -euo pipefail
    latest=$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
    major=$(echo "$latest" | sed 's/^v//' | cut -d. -f1)
    minor=$(echo "$latest" | sed 's/^v//' | cut -d. -f2)
    patch=$(echo "$latest" | sed 's/^v//' | cut -d. -f3)
    case "{{bump}}" in
      major) major=$((major + 1)); minor=0; patch=0 ;;
      minor) minor=$((minor + 1)); patch=0 ;;
      patch) patch=$((patch + 1)) ;;
      *) echo "Usage: just release [patch|minor|major]" >&2; exit 1 ;;
    esac
    new_tag="v${major}.${minor}.${patch}"
    echo "Tagging $new_tag (was $latest)"
    git tag -a "$new_tag" -m "Release $new_tag"
    git push origin "$new_tag"
