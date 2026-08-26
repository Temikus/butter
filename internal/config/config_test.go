package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	path := writeTestConfig(t, `
server:
  address: ":9090"
  read_timeout: 10s
  write_timeout: 60s

providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-test-123"
        weight: 3

routing:
  default_provider: openrouter
  failover:
    enabled: true
    max_retries: 5
    retry_on: [429, 500]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Address != ":9090" {
		t.Errorf("expected :9090, got %s", cfg.Server.Address)
	}
	if cfg.Server.ReadTimeout != 10*time.Second {
		t.Errorf("expected 10s, got %v", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 60*time.Second {
		t.Errorf("expected 60s, got %v", cfg.Server.WriteTimeout)
	}
	if cfg.Routing.DefaultProvider != "openrouter" {
		t.Errorf("expected openrouter, got %s", cfg.Routing.DefaultProvider)
	}
	if cfg.Routing.Failover.MaxRetries != 5 {
		t.Errorf("expected 5 retries, got %d", cfg.Routing.Failover.MaxRetries)
	}
	if len(cfg.Routing.Failover.RetryOn) != 2 {
		t.Errorf("expected 2 retry codes, got %d", len(cfg.Routing.Failover.RetryOn))
	}

	p := cfg.Providers["openrouter"]
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-test-123" || p.Keys[0].Weight != 3 {
		t.Errorf("unexpected key config: %+v", p.Keys)
	}
}

func TestEnvVarSubstitution(t *testing.T) {
	t.Setenv("BUTTER_TEST_KEY", "sk-from-env")

	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "${BUTTER_TEST_KEY}"
routing:
  default_provider: openrouter
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Providers["openrouter"].Keys[0].Key != "sk-from-env" {
		t.Errorf("env var not substituted, got: %s", cfg.Providers["openrouter"].Keys[0].Key)
	}
}

func TestUnsetEnvVarFailsLoad(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "${THIS_VAR_DOES_NOT_EXIST}"
routing:
  default_provider: openrouter
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an unset env var, got nil")
	}
	if !strings.Contains(err.Error(), "THIS_VAR_DOES_NOT_EXIST") {
		t.Errorf("error should name the unset variable, got: %v", err)
	}
}

func TestUnsetEnvVarsAllReported(t *testing.T) {
	t.Setenv("BUTTER_SET_VAR", "sk-set")

	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: "${BUTTER_MISSING_URL}"
    keys:
      - key: "${BUTTER_SET_VAR}"
      - key: "${BUTTER_MISSING_KEY}"
      - key: "${BUTTER_MISSING_KEY}"
routing:
  default_provider: openrouter
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for unset env vars, got nil")
	}
	msg := err.Error()
	for _, name := range []string{"BUTTER_MISSING_URL", "BUTTER_MISSING_KEY"} {
		if !strings.Contains(msg, name) {
			t.Errorf("error should name %s, got: %v", name, err)
		}
	}
	if strings.Contains(msg, "BUTTER_SET_VAR") {
		t.Errorf("error should not name a set variable, got: %v", err)
	}
	if n := strings.Count(msg, "BUTTER_MISSING_KEY"); n != 1 {
		t.Errorf("repeated variable should be reported once, got %d occurrences", n)
	}
}

func TestUnsetEnvVarInCommentIgnored(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-test"
  # groq:
  #   base_url: https://api.groq.com/openai/v1
  #   keys:
  #     - key: "${BUTTER_COMMENTED_KEY}"
routing:
  default_provider: openrouter
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(cfg.Providers))
	}
}

func TestDefaults(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-test"
routing:
  default_provider: openrouter
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Address != ":8080" {
		t.Errorf("expected default :8080, got %s", cfg.Server.Address)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("expected default 30s read timeout, got %v", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 120*time.Second {
		t.Errorf("expected default 120s write timeout, got %v", cfg.Server.WriteTimeout)
	}
	if cfg.Server.MaxRequestBytes != DefaultMaxRequestBytes {
		t.Errorf("expected default max request bytes %d, got %d", DefaultMaxRequestBytes, cfg.Server.MaxRequestBytes)
	}
	if cfg.Routing.Failover.MaxRetries != 3 {
		t.Errorf("expected default 3 retries, got %d", cfg.Routing.Failover.MaxRetries)
	}
	if cfg.Providers["openrouter"].Keys[0].Weight != 1 {
		t.Errorf("expected default weight 1, got %d", cfg.Providers["openrouter"].Keys[0].Weight)
	}
	if cfg.Providers["openrouter"].CredentialMode != "stored" {
		t.Errorf("expected default credential_mode 'stored', got %q", cfg.Providers["openrouter"].CredentialMode)
	}
}

func TestCredentialModePassthrough(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  anthropic:
    credential_mode: passthrough
routing:
  default_provider: anthropic
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Providers["anthropic"].CredentialMode != "passthrough" {
		t.Errorf("expected credential_mode 'passthrough', got %q", cfg.Providers["anthropic"].CredentialMode)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	path := writeTestConfig(t, `{{invalid yaml`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadEmptyProviders(t *testing.T) {
	path := writeTestConfig(t, `
server:
  address: ":9090"
routing:
  default_provider: ""
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected empty providers, got %d", len(cfg.Providers))
	}
}

func TestMultipleEnvVars(t *testing.T) {
	t.Setenv("BUTTER_KEY_1", "sk-first")
	t.Setenv("BUTTER_KEY_2", "sk-second")
	t.Setenv("BUTTER_URL", "https://custom.api/v1")

	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: "${BUTTER_URL}"
    keys:
      - key: "${BUTTER_KEY_1}"
      - key: "${BUTTER_KEY_2}"
routing:
  default_provider: openrouter
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := cfg.Providers["openrouter"]
	if p.BaseURL != "https://custom.api/v1" {
		t.Errorf("expected custom URL, got %s", p.BaseURL)
	}
	if len(p.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(p.Keys))
	}
	if p.Keys[0].Key != "sk-first" {
		t.Errorf("expected sk-first, got %s", p.Keys[0].Key)
	}
	if p.Keys[1].Key != "sk-second" {
		t.Errorf("expected sk-second, got %s", p.Keys[1].Key)
	}
}

func TestMultipleProvidersAndRoutes(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-or"
  openai:
    base_url: https://api.openai.com/v1
    keys:
      - key: "sk-oai"
  anthropic:
    base_url: https://api.anthropic.com/v1
    keys:
      - key: "sk-ant"
routing:
  default_provider: openrouter
  models:
    gpt-4o:
      providers: [openai]
      strategy: priority
    claude-3-opus:
      providers: [anthropic, openrouter]
      strategy: priority
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(cfg.Providers))
	}
	if len(cfg.Routing.Models) != 2 {
		t.Fatalf("expected 2 model routes, got %d", len(cfg.Routing.Models))
	}

	claudeRoute := cfg.Routing.Models["claude-3-opus"]
	if len(claudeRoute.Providers) != 2 {
		t.Errorf("expected 2 providers for claude route, got %d", len(claudeRoute.Providers))
	}
	if claudeRoute.Providers[0] != "anthropic" {
		t.Errorf("expected anthropic first, got %s", claudeRoute.Providers[0])
	}
}

func TestKeyWeightDefaults(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-1"
      - key: "sk-2"
      - key: "sk-3"
        weight: 5
routing:
  default_provider: openrouter
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	keys := cfg.Providers["openrouter"].Keys
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(keys))
	}
	if keys[0].Weight != 1 {
		t.Errorf("key 0: expected default weight 1, got %d", keys[0].Weight)
	}
	if keys[1].Weight != 1 {
		t.Errorf("key 1: expected default weight 1, got %d", keys[1].Weight)
	}
	if keys[2].Weight != 5 {
		t.Errorf("key 2: expected weight 5, got %d", keys[2].Weight)
	}
}

func TestWASMPluginBoundsDefaults(t *testing.T) {
	path := writeTestConfig(t, `
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-1"
routing:
  default_provider: openrouter
wasm_plugins:
  - name: defaults
    path: ./a.wasm
  - name: explicit
    path: ./b.wasm
    timeout: 1500ms
    max_pages: 64
  - name: unbounded
    path: ./c.wasm
    timeout: -1s
    max_pages: -1
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.WASMPlugins) != 3 {
		t.Fatalf("expected 3 wasm plugins, got %d", len(cfg.WASMPlugins))
	}

	if got := cfg.WASMPlugins[0].Timeout; got != DefaultWASMTimeout {
		t.Errorf("defaults: timeout = %v, want %v", got, DefaultWASMTimeout)
	}
	if got := cfg.WASMPlugins[0].MaxPages; got != DefaultWASMMaxPages {
		t.Errorf("defaults: max_pages = %d, want %d", got, DefaultWASMMaxPages)
	}

	if got := cfg.WASMPlugins[1].Timeout; got != 1500*time.Millisecond {
		t.Errorf("explicit: timeout = %v, want 1.5s", got)
	}
	if got := cfg.WASMPlugins[1].MaxPages; got != 64 {
		t.Errorf("explicit: max_pages = %d, want 64", got)
	}

	// Negative values are an explicit opt-out and must survive defaulting.
	if got := cfg.WASMPlugins[2].Timeout; got != -1*time.Second {
		t.Errorf("unbounded: timeout = %v, want -1s", got)
	}
	if got := cfg.WASMPlugins[2].MaxPages; got != -1 {
		t.Errorf("unbounded: max_pages = %d, want -1", got)
	}
}
