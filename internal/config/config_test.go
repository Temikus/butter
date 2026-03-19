package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
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
`), 0644)

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

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "${BUTTER_TEST_KEY}"
routing:
  default_provider: openrouter
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Providers["openrouter"].Keys[0].Key != "sk-from-env" {
		t.Errorf("env var not substituted, got: %s", cfg.Providers["openrouter"].Keys[0].Key)
	}
}

func TestUnsetEnvVarPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "${THIS_VAR_DOES_NOT_EXIST}"
routing:
  default_provider: openrouter
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Providers["openrouter"].Keys[0].Key != "${THIS_VAR_DOES_NOT_EXIST}" {
		t.Errorf("unset env var should be preserved, got: %s", cfg.Providers["openrouter"].Keys[0].Key)
	}
}

func TestDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
providers:
  openrouter:
    base_url: https://openrouter.ai/api/v1
    keys:
      - key: "sk-test"
routing:
  default_provider: openrouter
`), 0644)

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
	if cfg.Routing.Failover.MaxRetries != 3 {
		t.Errorf("expected default 3 retries, got %d", cfg.Routing.Failover.MaxRetries)
	}
	if cfg.Providers["openrouter"].Keys[0].Weight != 1 {
		t.Errorf("expected default weight 1, got %d", cfg.Providers["openrouter"].Keys[0].Weight)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`{{invalid yaml`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadEmptyProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
server:
  address: ":9090"
routing:
  default_provider: ""
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Providers != nil && len(cfg.Providers) != 0 {
		t.Errorf("expected empty providers, got %d", len(cfg.Providers))
	}
}

func TestMultipleEnvVars(t *testing.T) {
	t.Setenv("BUTTER_KEY_1", "sk-first")
	t.Setenv("BUTTER_KEY_2", "sk-second")
	t.Setenv("BUTTER_URL", "https://custom.api/v1")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
providers:
  openrouter:
    base_url: "${BUTTER_URL}"
    keys:
      - key: "${BUTTER_KEY_1}"
      - key: "${BUTTER_KEY_2}"
routing:
  default_provider: openrouter
`), 0644)

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
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
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
`), 0644)

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
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
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
`), 0644)

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
