// Package config loads githints runtime configuration from .githints/config.json
// with environment-variable overrides. It is intentionally small and dependency-free:
// no Viper, no TOML parsers, just the Go standard library.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// Ollama holds the optional local-LLM summarization settings. Every field has
// a safe default and the whole block is opt-in: Enabled defaults to false so
// fresh installs make zero network calls.
type Ollama struct {
	Enabled      bool   `json:"enabled"`
	Endpoint     string `json:"endpoint"`
	Model        string `json:"model"`
	TimeoutMS    int    `json:"timeout_ms"`
	MaxDiffBytes int    `json:"max_diff_bytes"`
}

type Config struct {
	Ollama Ollama `json:"ollama"`
}

const (
	defaultEndpoint     = "http://127.0.0.1:11434"
	defaultModel        = "qwen2.5:3b-instruct"
	defaultTimeoutMS    = 3000
	defaultMaxDiffBytes = 4096

	// Escape hatch for non-loopback endpoints. Checked at config load time so
	// validation fails closed by default.
	allowNonLoopbackEnv = "GITHINTS_OLLAMA_ALLOW_NON_LOOPBACK"
)

func Default() Config {
	return Config{
		Ollama: Ollama{
			Enabled:      false,
			Endpoint:     defaultEndpoint,
			Model:        defaultModel,
			TimeoutMS:    defaultTimeoutMS,
			MaxDiffBytes: defaultMaxDiffBytes,
		},
	}
}

// Load reads .githints/config.json from root (if present), applies
// GITHINTS_OLLAMA_* environment overrides, and validates the result.
// Validation is fail-closed: a non-loopback endpoint with no override is an
// error, but an entirely disabled Ollama block is accepted silently.
func Load(root string) (Config, error) {
	cfg := Default()

	path := filepath.Join(root, ".githints", "config.json")
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	applyEnvOverrides(&cfg)

	if cfg.Ollama.Enabled {
		if err := validateOllama(cfg.Ollama); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GITHINTS_OLLAMA_ENABLED"); v != "" {
		cfg.Ollama.Enabled = truthy(v)
	}
	if v := os.Getenv("GITHINTS_OLLAMA_ENDPOINT"); v != "" {
		cfg.Ollama.Endpoint = v
	}
	if v := os.Getenv("GITHINTS_OLLAMA_MODEL"); v != "" {
		cfg.Ollama.Model = v
	}
	if v := os.Getenv("GITHINTS_OLLAMA_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Ollama.TimeoutMS = n
		}
	}
	if v := os.Getenv("GITHINTS_OLLAMA_MAX_DIFF_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Ollama.MaxDiffBytes = n
		}
	}
}

func truthy(s string) bool {
	switch s {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	}
	return false
}

// validateOllama enforces the local-only security boundary. It rejects
// non-loopback endpoints unless the dedicated override env var is set.
func validateOllama(o Ollama) error {
	if o.Endpoint == "" {
		return fmt.Errorf("ollama.endpoint is required when ollama.enabled is true")
	}
	if o.Model == "" {
		return fmt.Errorf("ollama.model is required when ollama.enabled is true")
	}
	if o.TimeoutMS <= 0 {
		return fmt.Errorf("ollama.timeout_ms must be positive, got %d", o.TimeoutMS)
	}
	if o.MaxDiffBytes <= 0 {
		return fmt.Errorf("ollama.max_diff_bytes must be positive, got %d", o.MaxDiffBytes)
	}

	if os.Getenv(allowNonLoopbackEnv) == "1" {
		return nil
	}

	loopback, err := endpointIsLoopback(o.Endpoint)
	if err != nil {
		return fmt.Errorf("ollama.endpoint validation failed: %w (set %s=1 to bypass)", err, allowNonLoopbackEnv)
	}
	if !loopback {
		return fmt.Errorf("ollama.endpoint %q is not a loopback address; local Ollama only (set %s=1 to override)", o.Endpoint, allowNonLoopbackEnv)
	}
	return nil
}

// endpointIsLoopback parses endpoint and reports whether every resolved IP
// address for its hostname is a loopback address. Literal IPs are checked
// directly; hostnames are resolved so that "localhost" is accepted but a
// remote hostname is rejected.
func endpointIsLoopback(endpoint string) (bool, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false, fmt.Errorf("parse endpoint: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return false, fmt.Errorf("endpoint has no host")
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return false, fmt.Errorf("lookup host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return false, fmt.Errorf("host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}
