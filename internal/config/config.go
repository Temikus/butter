package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig              `yaml:"server"`
	Providers   map[string]ProviderConfig `yaml:"providers"`
	Routing     RoutingConfig             `yaml:"routing"`
	Plugins     map[string]map[string]any `yaml:"plugins,omitempty"`
	WASMPlugins []WASMPluginConfig        `yaml:"wasm_plugins,omitempty"`
	Cache       CacheConfig               `yaml:"cache"`
	AppKeys     AppKeysConfig             `yaml:"app_keys,omitempty"`
}

// AppKeysConfig controls the optional application-key tracking feature.
// When Enabled is false (default) there is zero runtime overhead.
type AppKeysConfig struct {
	Enabled     bool              `yaml:"enabled"`
	RequireKey  bool              `yaml:"require_key"`
	Header      string            `yaml:"header"`
	DefaultTTL  time.Duration     `yaml:"default_ttl"` // applied to vended keys when ttl_seconds is omitted; 0 = no default
	Keys        []AppKeyEntry     `yaml:"keys,omitempty"`
	Persistence AppKeyPersistence `yaml:"persistence,omitempty"`
}

// AppKeyPersistence configures optional bbolt-backed durable storage for
// application keys and their usage counters. When enabled, keys and counters
// survive process restarts. The hot path is unaffected — all request-time
// operations use in-memory atomics; bbolt is write-behind only.
type AppKeyPersistence struct {
	Enabled       bool          `yaml:"enabled"`
	Path          string        `yaml:"path"`           // bbolt file path, default "butter-appkeys.db"
	FlushInterval time.Duration `yaml:"flush_interval"` // how often counters are flushed to disk, default 30s
}

// AppKeyEntry represents a pre-provisioned application key in config.
type AppKeyEntry struct {
	Key              string   `yaml:"key"`
	Label            string   `yaml:"label,omitempty"`
	AllowedModels    []string `yaml:"allowed_models,omitempty"`
	AllowedProviders []string `yaml:"allowed_providers,omitempty"`
}

// WASMPluginConfig holds the configuration for a single WASM plugin.
type WASMPluginConfig struct {
	// Name is the unique identifier for this plugin instance (used in logs).
	Name string `yaml:"name"`
	// Path is the filesystem path to the compiled .wasm file.
	Path string `yaml:"path"`
	// Config is forwarded to the WASM plugin via the Extism manifest config.
	// Values are accessible inside the plugin via the Extism PDK config API.
	Config map[string]string `yaml:"config,omitempty"`
	// Timeout bounds a single hook invocation. Defaults to
	// DefaultWASMTimeout; a negative value disables the bound (not
	// recommended — an infinite loop in a hook then hangs the request
	// until the client disconnects).
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// MaxPages caps the plugin's linear memory in 64 KiB WASM pages.
	// Defaults to DefaultWASMMaxPages. A negative value disables the cap.
	MaxPages int `yaml:"max_pages,omitempty"`
}

const (
	// DefaultWASMTimeout bounds a single WASM hook invocation. Hooks run
	// inline on the request path, so the bound is well under any sensible
	// client deadline.
	DefaultWASMTimeout = 5 * time.Second
	// DefaultWASMMaxPages caps WASM linear memory at 512 pages (32 MiB).
	DefaultWASMMaxPages = 512
)

type CacheConfig struct {
	Enabled    bool          `yaml:"enabled"`
	Backend    string        `yaml:"backend"` // "memory" (default) or "redis"
	TTL        time.Duration `yaml:"ttl"`
	MaxEntries int           `yaml:"max_entries"` // memory backend only
	Redis      RedisConfig   `yaml:"redis,omitempty"`
}

// RedisConfig holds connection settings for the Redis cache backend.
type RedisConfig struct {
	Address   string `yaml:"address"` // e.g. "localhost:6379"
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"` // default "butter:"
}

type ServerConfig struct {
	Address      string        `yaml:"address"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	// ReadHeaderTimeout bounds how long a client may take to send request
	// headers. Without it a slowloris client can hold a connection open
	// indefinitely by dribbling header bytes. Defaults to
	// DefaultReadHeaderTimeout.
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	// IdleTimeout bounds how long an idle keep-alive connection is kept.
	// Defaults to DefaultIdleTimeout.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// MaxHeaderBytes caps the size of request headers. Defaults to
	// DefaultMaxHeaderBytes.
	MaxHeaderBytes int `yaml:"max_header_bytes"`
	// MaxRequestBytes caps the size of an inbound request body (bytes). Requests
	// exceeding it receive 413. Defaults to DefaultMaxRequestBytes. A negative
	// value disables the cap; 0 is treated as unset and gets the default.
	MaxRequestBytes int64 `yaml:"max_request_bytes"`
}

// DefaultMaxRequestBytes is the default inbound request body cap: 32 MiB. This
// matches the Anthropic Messages API's documented 32 MB hard limit, so the proxy
// accepts any multimodal/base64 payload the upstream providers themselves accept
// while bounding per-request memory against exhaustion DoS.
const DefaultMaxRequestBytes int64 = 32 << 20

// Slowloris defaults. ReadHeaderTimeout is deliberately much shorter than
// ReadTimeout: headers are small and arrive fast, bodies may not.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	// DefaultMaxHeaderBytes matches net/http's own default (1 MiB), set
	// explicitly so the value is visible and operator-tunable.
	DefaultMaxHeaderBytes = 1 << 20
)

type ProviderConfig struct {
	BaseURL        string            `yaml:"base_url"`
	Keys           []KeyConfig       `yaml:"keys"`
	CredentialMode string            `yaml:"credential_mode,omitempty"` // "stored" (default) | "passthrough"
	Region         string            `yaml:"region,omitempty"`          // AWS region (Bedrock)
	AWSProfile     string            `yaml:"aws_profile,omitempty"`     // AWS shared config profile (Bedrock)
	ModelMap       map[string]string `yaml:"model_map,omitempty"`       // Anthropic→Bedrock model ID overrides
	APIVersion     string            `yaml:"api_version,omitempty"`     // API version query param (Azure OpenAI)
}

type KeyConfig struct {
	Key    string   `yaml:"key"`
	Weight int      `yaml:"weight"`
	Models []string `yaml:"models,omitempty"`
}

type RoutingConfig struct {
	DefaultProvider string                `yaml:"default_provider"`
	Models          map[string]ModelRoute `yaml:"models,omitempty"`
	Failover        FailoverConfig        `yaml:"failover"`
}

type ModelRoute struct {
	Providers []string `yaml:"providers"`
	Strategy  string   `yaml:"strategy"` // priority | round-robin | weighted
}

type FailoverConfig struct {
	Enabled    bool          `yaml:"enabled"`
	MaxRetries int           `yaml:"max_retries"`
	Backoff    BackoffConfig `yaml:"backoff"`
	RetryOn    []int         `yaml:"retry_on"`
}

type BackoffConfig struct {
	Initial    time.Duration `yaml:"initial"`
	Multiplier float64       `yaml:"multiplier"`
	Max        time.Duration `yaml:"max"`
}

var envVarRegex = regexp.MustCompile(`\$\{([^}]+)\}`)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator-supplied -config flag, not untrusted input. #nosec G304
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	expanded, missing := expandEnv(string(data))
	if len(missing) > 0 {
		return nil, fmt.Errorf("unset environment variables in config: %s", strings.Join(missing, ", "))
	}

	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	applyDefaults(cfg)
	return cfg, nil
}

// expandEnv substitutes ${VAR} references with their environment values and
// returns the names of every unset variable, in order of first appearance.
// References on lines that are wholly commented out are left alone, so a
// template config's disabled provider blocks don't demand keys the operator
// isn't using. Trailing comments are not stripped: a ${VAR} after a `#` on an
// otherwise live line is still substituted, and still reported when unset.
func expandEnv(data string) (string, []string) {
	var missing []string
	seen := make(map[string]struct{})

	lines := strings.Split(data, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines[i] = envVarRegex.ReplaceAllStringFunc(line, func(match string) string {
			name := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
			if val, ok := os.LookupEnv(name); ok {
				return val
			}
			if _, dup := seen[name]; !dup {
				seen[name] = struct{}{}
				missing = append(missing, name)
			}
			return match
		})
	}
	return strings.Join(lines, "\n"), missing
}

func applyDefaults(cfg *Config) { //nolint:gocyclo // flat sequence of per-field default assignments
	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * time.Second
	}
	if cfg.Server.ReadHeaderTimeout == 0 {
		cfg.Server.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.Server.MaxHeaderBytes == 0 {
		cfg.Server.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if cfg.Server.MaxRequestBytes == 0 {
		cfg.Server.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if cfg.Routing.Failover.MaxRetries == 0 {
		cfg.Routing.Failover.MaxRetries = 3
	}
	if cfg.Routing.Failover.Backoff.Initial == 0 {
		cfg.Routing.Failover.Backoff.Initial = 100 * time.Millisecond
	}
	if cfg.Routing.Failover.Backoff.Multiplier == 0 {
		cfg.Routing.Failover.Backoff.Multiplier = 2.0
	}
	if cfg.Routing.Failover.Backoff.Max == 0 {
		cfg.Routing.Failover.Backoff.Max = 5 * time.Second
	}
	if cfg.Cache.Enabled {
		if cfg.Cache.Backend == "" {
			cfg.Cache.Backend = "memory"
		}
		if cfg.Cache.TTL == 0 {
			cfg.Cache.TTL = 5 * time.Minute
		}
		if cfg.Cache.Backend == "memory" && cfg.Cache.MaxEntries == 0 {
			cfg.Cache.MaxEntries = 10000
		}
		if cfg.Cache.Backend == "redis" && cfg.Cache.Redis.KeyPrefix == "" {
			cfg.Cache.Redis.KeyPrefix = "butter:"
		}
	}
	if cfg.AppKeys.Header == "" {
		cfg.AppKeys.Header = "X-Butter-App-Key"
	}
	if cfg.AppKeys.Persistence.Enabled {
		if cfg.AppKeys.Persistence.Path == "" {
			cfg.AppKeys.Persistence.Path = "butter-appkeys.db"
		}
		if cfg.AppKeys.Persistence.FlushInterval == 0 {
			cfg.AppKeys.Persistence.FlushInterval = 30 * time.Second
		}
	}
	for i := range cfg.WASMPlugins {
		if cfg.WASMPlugins[i].Timeout == 0 {
			cfg.WASMPlugins[i].Timeout = DefaultWASMTimeout
		}
		if cfg.WASMPlugins[i].MaxPages == 0 {
			cfg.WASMPlugins[i].MaxPages = DefaultWASMMaxPages
		}
	}
	for name, p := range cfg.Providers {
		if p.CredentialMode == "" {
			p.CredentialMode = "stored"
		}
		for i := range p.Keys {
			if p.Keys[i].Weight == 0 {
				p.Keys[i].Weight = 1
			}
		}
		cfg.Providers[name] = p
	}
}
