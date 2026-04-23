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
	Enabled     bool                `yaml:"enabled"`
	RequireKey  bool                `yaml:"require_key"`
	Header      string              `yaml:"header"`
	Keys        []AppKeyEntry       `yaml:"keys,omitempty"`
	Persistence AppKeyPersistence   `yaml:"persistence,omitempty"`
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
	Key   string `yaml:"key"`
	Label string `yaml:"label,omitempty"`
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
}

type CacheConfig struct {
	Enabled    bool          `yaml:"enabled"`
	Backend    string        `yaml:"backend"`     // "memory" (default) or "redis"
	TTL        time.Duration `yaml:"ttl"`
	MaxEntries int           `yaml:"max_entries"` // memory backend only
	Redis      RedisConfig   `yaml:"redis,omitempty"`
}

// RedisConfig holds connection settings for the Redis cache backend.
type RedisConfig struct {
	Address   string `yaml:"address"`    // e.g. "localhost:6379"
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"` // default "butter:"
}

type ServerConfig struct {
	Address      string        `yaml:"address"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type ProviderConfig struct {
	BaseURL        string      `yaml:"base_url"`
	Keys           []KeyConfig `yaml:"keys"`
	CredentialMode string      `yaml:"credential_mode,omitempty"` // "stored" (default) | "passthrough"
}

type KeyConfig struct {
	Key    string   `yaml:"key"`
	Weight int      `yaml:"weight"`
	Models []string `yaml:"models,omitempty"`
}

type RoutingConfig struct {
	DefaultProvider string                 `yaml:"default_provider"`
	Models          map[string]ModelRoute  `yaml:"models,omitempty"`
	Failover        FailoverConfig         `yaml:"failover"`
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Substitute environment variables
	expanded := envVarRegex.ReplaceAllStringFunc(string(data), func(match string) string {
		varName := strings.TrimSuffix(strings.TrimPrefix(match, "${"), "}")
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match
	})

	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	applyDefaults(cfg)
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * time.Second
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
