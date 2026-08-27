# Prompt Injection Guard

A WASM plugin for Butter that scans chat completion requests for known prompt injection patterns before they reach the upstream provider. It operates at the `pre_http` hook — the earliest interception point in the plugin chain.

This is one layer of defense-in-depth. It catches known attack signatures but cannot detect novel attacks, indirect injection via RAG documents, or multi-session jailbreaks. Combine with provider-side guardrails for broader coverage.

## Configuration

```yaml
wasm_plugins:
  - name: prompt-injection-guard
    path: ./plugins/prompt-injection-guard/prompt-injection-guard.wasm
    timeout: 5s                  # per-hook execution bound (default)
    max_pages: 512               # linear memory cap, 64 KiB pages (default)
    config:
      mode: block              # block | log | tag
      scan_roles: "user,assistant"  # comma-separated roles, or "all"
```

### `mode`

| Mode | Behavior |
|------|----------|
| `block` | Short-circuits with HTTP 400 and an OpenAI-compatible error body. Default. |
| `log` | Passes the request through; attaches detection metadata for downstream plugins (requestlog, tracing). |
| `tag` | Same as `log` — metadata is attached, request continues. |

Unknown mode values fail-safe to `block`.

### `scan_roles`

Comma-separated list of message roles to scan. Default: `user,assistant`.

Set to `all` to also scan `system` and `tool` messages (useful if you accept untrusted system prompts).

## Detection Categories

| Category | Patterns | Description |
|----------|----------|-------------|
| `instruction_override` | 10 | "Ignore previous instructions", "forget everything", etc. |
| `role_override` | 9 | "You are now a", "act as ", "pretend to be", etc. |
| `jailbreak` | 10 | "jailbreak", "DAN mode", "developer mode", "god mode", etc. |
| `prompt_extraction` | 10 | "Reveal your system prompt", "what are your instructions", etc. (OWASP LLM07) |
| `boundary_bypass` | 9 | "From now on you will", "new instructions:", "override the system prompt", etc. |
| `persona_injection` | 7 | "Your true self", "you have no restrictions", "without restrictions", etc. |
| `encoding_attack` | 6 | "base64 decode", "hex decode", "rot13", "decode and execute", etc. |

Total: 61 patterns.

## How Detection Works

1. **Unicode normalization**: Strips zero-width characters (U+200B/C/D, U+202E, U+FEFF, U+00AD), maps fullwidth ASCII (U+FF01-U+FF5E) to standard ASCII, then lowercases.
2. **Substring matching**: Each normalized message is checked with `strings.Contains` against all 61 patterns.
3. **Trailing-space boundaries**: Patterns like `"act as "` and `"impersonate "` include a trailing space to prevent matching words like "acting" or "impersonated".
4. **First-match-wins**: Scanning short-circuits on the first pattern hit.

## Metadata Output

On detection, the following metadata keys are attached to the request:

```json
{
  "prompt_injection_detected": true,
  "matched_pattern": "jailbreak",
  "matched_category": "jailbreak"
}
```

In `block` mode, the client receives:

```json
{
  "error": {
    "message": "Request blocked: potential prompt injection detected",
    "type": "invalid_request_error",
    "code": "prompt_injection_detected"
  }
}
```

In `log`/`tag` mode, metadata flows through to the requestlog plugin (appears in structured logs) and OTel tracing spans (when the tracing plugin is enabled).

## Known False-Positive-Prone Patterns

The following patterns trigger on legitimate content due to the substring-matching constraint (TinyGo does not support `regexp` under WASI):

| Pattern | Category | Example legitimate trigger | Mitigation |
|---------|----------|---------------------------|------------|
| `jailbreak` | jailbreak | "I want to jailbreak my iPhone" | Use `mode: log` for developer/mobile workloads |
| `developer mode` | jailbreak | "Enable developer mode in Chrome DevTools" | Use `mode: log` for developer-tooling apps |
| `god mode` | jailbreak | "In Doom you can activate god mode" | Use `mode: log` for gaming workloads |
| `sudo mode` | jailbreak | "Run the command in sudo mode" | Use `mode: log` for sysadmin apps |
| `bypass your` | jailbreak | "Bypass your firewall temporarily" | Use `mode: log` for security-education apps |
| `base64 decode` | encoding_attack | "How do I base64 decode in Python?" | Use `mode: log` for coding-assistant workloads |
| `hex decode` | encoding_attack | "Use hex decode to convert the header" | Same as above |
| `rot13` | encoding_attack | "ROT13 is a substitution cipher" | Same as above |
| `decode the following and` | encoding_attack | "Decode the following and convert to UTF-8" | Same as above |
| `from now on you will` | boundary_bypass | "From now on you will need a badge" | Narrow `scan_roles` to `user` only |
| `from now on, you will` | boundary_bypass | "From now on, you will receive updates" | Same as above |
| `new instructions:` | boundary_bypass | "See the new instructions: section" | Use `mode: log` for email/docs apps |
| `updated instructions:` | boundary_bypass | "Check the updated instructions: page" | Same as above |
| `without restrictions` | persona_injection | "Available without restrictions under MIT" | Use `mode: log` for legal/licensing apps |
| `forget everything` | instruction_override | "Forget everything I said, let's start fresh" | Accept trade-off or use `mode: log` |

These cannot be fixed without regex or negative-lookahead support. The trade-off is intentional: false positives in `block` mode are preferable to missed attacks for security-sensitive deployments. For workloads with high legitimate overlap, use `log` or `tag` mode and filter downstream.

## Tuning Recommendations

| Workload | Recommendation |
|----------|----------------|
| Developer/coding assistants | `mode: log` — encoding_attack and jailbreak categories have high FP rate |
| Gaming apps | `mode: log` — "god mode", "jailbreak" are common game terminology |
| Security education | `mode: log` — encoding_attack patterns overlap with curriculum |
| Internal tools (trusted users) | `mode: tag` with narrow `scan_roles: user` |
| Public-facing APIs (untrusted input) | `mode: block` (default) — accept FP trade-off for security |
| Multi-tenant with mixed trust | `mode: block` + provider-side guardrails as second layer |

Additional tuning levers:
- Narrow `scan_roles` to `"user"` if assistant-role injection is not a concern for your threat model.
- Set `scan_roles: "all"` if you accept untrusted system prompts (e.g., user-configurable system messages).
- Combine with the ratelimit plugin to throttle clients that repeatedly trigger detection in `log` mode.

## Limitations

- No regex support (TinyGo/WASI constraint) — all matching is plain substring
- Cannot detect indirect/RAG injection (payloads embedded in retrieved documents)
- Cannot detect novel paraphrased attacks with no known signature
- Cannot detect multi-session slow-burn jailbreaks
- Not a replacement for model-level safety alignment or provider guardrails
- No per-pattern enable/disable (all 61 patterns are always active)

## Building and Testing

```bash
# Build the WASM binary (requires TinyGo >= 0.34)
just build-injection-guard

# Run unit tests (host-side, no WASM runtime needed)
go test ./plugins/prompt-injection-guard/...

# Run with verbose output to see known-FP documentation
go test ./plugins/prompt-injection-guard/... -v -run FalsePositive
```
