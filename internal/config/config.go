// Package config loads githints runtime configuration from .githints/config.json
// with environment-variable overrides. It is intentionally small and dependency-free:
// no Viper, no TOML parsers, just the Go standard library.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cjrdz/githints/internal/safefs"
)

// Config is .githints/config.json. Unknown keys are ignored, so a file that
// still sets a removed one (the "ollama" block of earlier releases) loads unchanged.
type Config struct {
	Index  Index  `json:"index"`
	Review Review `json:"review"`
}

// Review configures risk-scaled review (see internal/risk). Rules add a class
// to the paths they match; they cannot lower a level, because this file
// arrives in clones. Whether review applies at all is the user's choice, made
// with GITHINTS_REVIEW=0, never by a repository.
type Review struct {
	// Gate is what the pre-commit hook does with a high-risk change that has
	// no approved review receipt: "" (nothing), "warn" or "block".
	Gate  string       `json:"gate"`
	Rules []ReviewRule `json:"rules"`
}

// ReviewRule marks paths matching Match ("dir/**", a path glob, or a file-name
// glob) as Class.
type ReviewRule struct {
	Match string `json:"match"`
	Class string `json:"class"`
}

const (
	defaultIndexMaxBytes       = 200 * 1024 * 1024
	defaultIndexMaxFileSize    = 1024 * 1024
	defaultIndexParseTimeoutMS = 5000
)

// Index controls the optional structural code index. Enabled defaults to true
// so the index is built incrementally on every commit; the flag exists as an
// opt-out for unusually large repos or unusual workflows.
type Index struct {
	Enabled        bool     `json:"enabled"`
	Languages      []string `json:"languages"`
	MaxBytes       int      `json:"max_bytes"`
	MaxFileSize    int      `json:"max_file_size"`
	ParseTimeoutMS int      `json:"parse_timeout_ms"`
	// GraphHTML regenerates .githints/graph.html, the offline dependency
	// graph viewer, whenever the index is rebuilt or updated by the hook.
	GraphHTML bool `json:"graph_html"`
}

func Default() Config {
	return Config{
		Index: Index{
			Enabled: true,
			// lang.AutoLanguages: every language the binary supports. A fixed
			// default (["go"] through v0.2.3) indexed nothing in a Python
			// repository.
			Languages:      []string{"auto"},
			MaxBytes:       defaultIndexMaxBytes,
			MaxFileSize:    defaultIndexMaxFileSize,
			ParseTimeoutMS: defaultIndexParseTimeoutMS,
		},
	}
}

// Load reads .githints/config.json from root (if present), applies
// GITHINTS_* environment overrides, and validates the result.
// Index settings are validated only when the index is enabled.
func Load(root string) (Config, error) {
	cfg := Default()

	path := filepath.Join(root, ".githints", "config.json")
	// config.json arrives in clones, so it is read like any other untrusted
	// file: through an os.Root, regular files only, size-capped.
	if data, err := safefs.ReadFile(filepath.Join(root, ".githints"), "config.json", maxConfigBytes); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}

	applyEnvOverrides(&cfg)

	if cfg.Index.Enabled {
		if err := validateIndex(cfg.Index); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("GITHINTS_INDEX_ENABLED"); v != "" {
		cfg.Index.Enabled = truthy(v)
	}
	if v := os.Getenv("GITHINTS_INDEX_LANGUAGES"); v != "" {
		// A value that parses to nothing ("," or "  ") is treated as garbage
		// and ignored, matching how the numeric overrides above ignore input
		// strconv rejects. Blanking the list instead would surface as
		// "index.languages is empty", which points at config.json and would
		// send the user looking in the wrong place.
		if langs := parseLanguageList(v); len(langs) > 0 {
			cfg.Index.Languages = langs
		}
	}
	if v := os.Getenv("GITHINTS_INDEX_MAX_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Index.MaxBytes = n
		}
	}
	if v := os.Getenv("GITHINTS_INDEX_MAX_FILE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Index.MaxFileSize = n
		}
	}
	if v := os.Getenv("GITHINTS_INDEX_PARSE_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Index.ParseTimeoutMS = n
		}
	}
	if v := os.Getenv("GITHINTS_INDEX_GRAPH_HTML"); v != "" {
		cfg.Index.GraphHTML = truthy(v)
	}
}

// parseLanguageList splits the comma-separated form used by
// GITHINTS_INDEX_LANGUAGES. Names are trimmed and lower-cased so "Go, TypeScript"
// and "go,typescript" agree, and duplicates are dropped. Empty entries are
// skipped rather than becoming a language named "", which would be reported
// later as an unsupported language with an empty name.
func parseLanguageList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func truthy(s string) bool {
	switch s {
	case "1", "true", "TRUE", "True", "yes", "YES", "on", "ON":
		return true
	}
	return false
}

// maxConfigBytes caps config.json. The real file is a few hundred bytes.
const maxConfigBytes = 1 << 20

// Upper bounds for the index settings. config.json travels in clones, so
// without them a repository could set a multi-gigabyte file cap or an
// hour-long parse timeout, and the post-commit hook would honor it.
const (
	maxIndexBytes        = 1 << 30
	maxIndexFileSize     = 64 << 20
	maxIndexParseTimeout = 60_000
)

// validateIndex enforces the bounds that keep the indexer from misbehaving.
// It is only called when index.enabled is true.
func validateIndex(i Index) error {
	if len(i.Languages) == 0 {
		return fmt.Errorf("index.languages is empty; add at least one supported language")
	}
	if i.MaxBytes <= 0 {
		return fmt.Errorf("index.max_bytes must be positive, got %d", i.MaxBytes)
	}
	if i.MaxFileSize <= 0 {
		return fmt.Errorf("index.max_file_size must be positive, got %d", i.MaxFileSize)
	}
	if i.ParseTimeoutMS <= 0 {
		return fmt.Errorf("index.parse_timeout_ms must be positive, got %d", i.ParseTimeoutMS)
	}
	if i.MaxBytes > maxIndexBytes {
		return fmt.Errorf("index.max_bytes must be at most %d, got %d", maxIndexBytes, i.MaxBytes)
	}
	if i.MaxFileSize > maxIndexFileSize {
		return fmt.Errorf("index.max_file_size must be at most %d, got %d", maxIndexFileSize, i.MaxFileSize)
	}
	if i.ParseTimeoutMS > maxIndexParseTimeout {
		return fmt.Errorf("index.parse_timeout_ms must be at most %d, got %d", maxIndexParseTimeout, i.ParseTimeoutMS)
	}
	return nil
}
