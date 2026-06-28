# Security Policy

## Supported Versions

Security is a core principle of Butter and we apply security patches across the
project. As Butter is still pre-1.0, we strongly recommend staying on the
**latest release** to receive all security updates.

| Version  | Supported          |
|----------|--------------------|
| latest   | :white_check_mark: |
| < latest | Best-effort        |

## Reporting a Vulnerability

**Please do not open a public issue for security vulnerabilities.**

Use [GitHub Private Vulnerability Reporting](https://github.com/temikus/butter/security/advisories/new)
to submit a report. This keeps the details confidential until a fix is available.

### What to include

- Description of the vulnerability and its impact.
- Steps to reproduce or a proof-of-concept.
- Affected version(s) and component (e.g., proxy engine, a provider adapter,
  the WASM plugin host, application-key store).
- Suggested fix, if you have one.

### What to expect

- **Acknowledgement** within 48 hours.
- **Status update** within 7 days with an assessment and expected timeline.
- A coordinated disclosure after the fix is released. Credit is given unless you
  prefer to remain anonymous.

## Security Architecture

Butter is an AI proxy gateway. Its security controls focus on credential
handling, tenant isolation, and untrusted plugin code.

### Application Keys

- Vended `btr_`-prefixed tokens with per-key usage tracking and an optional
  `require_key` enforcement mode.
- Full lifecycle: revoke, rotate, expiry (TTL), and hard purge.
- Per-key **scopes** (`allowed_models` / `allowed_providers`) enforced in the
  engine — denied model or provider returns `403`.
- Per-key rate limiting (priority: app-key > IP > global).
- A structured audit log records every lifecycle event.

### Credential Handling

- Provider API keys are supplied via config with `${ENV_VAR}` substitution —
  no secrets are committed to the repository.
- Per-provider `credential_mode`:
  - `stored` (default) — the gateway injects its own managed keys.
  - `passthrough` — the client's auth header is forwarded upstream and the
    gateway never stores the credential.
- AWS Bedrock authenticates with SigV4 (IAM), not static API keys.

### Plugin Sandboxing

- External plugins run as **WebAssembly** modules via the Extism/wazero host
  (pure Go). WASM modules are memory-isolated from the host and only reach
  capabilities the host explicitly grants.
- A built-in **prompt-injection-guard** WASM plugin scans chat messages for
  known injection patterns (block / log / tag modes).

### Supply Chain

- Release binaries are signed with [cosign](https://github.com/sigstore/cosign)
  (keyless OIDC); the checksums file carries a `.bundle` signature.
- Docker images are signed by immutable digest and carry **SLSA build
  provenance** attestations.
- **SBOMs** (syft) are generated for every release.
- [Renovate](https://docs.renovatebot.com/) keeps Go modules, Docker base
  images, and GitHub Actions up to date.
- All third-party GitHub Actions are pinned to full commit SHAs.

### CI Security Scanning

- **gosec** (SAST) — results uploaded to the GitHub Security tab via SARIF.
- **betterleaks** — secret detection on push and on a weekly schedule.
- **Grype** (Anchore) — dependency/filesystem scanning, plus container image
  scanning of the published distroless image (by digest, before signing).
