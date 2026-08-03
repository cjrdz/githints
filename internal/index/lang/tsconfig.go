package lang

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// TSPathsConfig holds the compilerOptions.paths mappings from a tsconfig.json
// (or jsconfig.json), used to resolve import specifiers like "@core/bff/proxy"
// to repo-relative paths so they link into the structural index graph instead
// of staying opaque alias strings.
type TSPathsConfig struct {
	// baseDir is the repo-relative directory targets resolve against: the
	// baseUrl value, or "" when unset (targets are then relative to the
	// tsconfig location, which LoadTSPathsConfig assumes is the repo root).
	baseDir string
	paths   map[string][]string
}

// LoadTSPathsConfig reads tsconfig.json (falling back to jsconfig.json) from
// root and returns its paths configuration, or nil when neither file exists,
// parsing fails, or no paths are configured. JSONC syntax (comments, trailing
// commas) is tolerated.
func LoadTSPathsConfig(root string) *TSPathsConfig {
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if cfg := parseTSPathsConfig(data); cfg != nil {
			return cfg
		}
	}
	return nil
}

type tsconfigJSON struct {
	CompilerOptions struct {
		BaseURL string              `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

func parseTSPathsConfig(data []byte) *TSPathsConfig {
	var cfg tsconfigJSON
	if err := json.Unmarshal(stripJSONC(data), &cfg); err != nil {
		return nil
	}
	if len(cfg.CompilerOptions.Paths) == 0 {
		return nil
	}
	base := cfg.CompilerOptions.BaseURL
	base = strings.TrimPrefix(base, "./")
	base = strings.TrimSuffix(base, "/")
	return &TSPathsConfig{baseDir: base, paths: cfg.CompilerOptions.Paths}
}

// Resolve maps a non-relative import specifier to a repo-relative path using
// the longest matching paths pattern. It returns ok=false when no pattern
// matches. Only the first target of a pattern is used, matching the common
// single-target setup; multi-target fallbacks would need filesystem probing.
func (c *TSPathsConfig) Resolve(spec string) (resolved string, ok bool) {
	if targets, hit := c.paths[spec]; hit && len(targets) > 0 {
		// Exact (non-wildcard) key match.
		return c.joinTarget(targets[0], ""), true
	}
	bestPrefix := -1
	var bestTargets []string
	var bestRest string
	for pattern, targets := range c.paths {
		star := strings.IndexByte(pattern, '*')
		if star < 0 {
			continue
		}
		prefix, suffix := pattern[:star], pattern[star+1:]
		if !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) {
			continue
		}
		if len(prefix) <= bestPrefix {
			continue
		}
		bestPrefix = len(prefix)
		bestTargets = targets
		bestRest = spec[len(prefix) : len(spec)-len(suffix)]
	}
	if bestPrefix < 0 || len(bestTargets) == 0 {
		return "", false
	}
	return c.joinTarget(bestTargets[0], bestRest), true
}

// joinTarget substitutes the wildcard match into the target pattern and
// normalizes the result to a repo-relative slash path.
func (c *TSPathsConfig) joinTarget(target, rest string) string {
	t := strings.Replace(target, "*", rest, 1)
	t = strings.TrimPrefix(t, "./")
	if c.baseDir != "" {
		t = c.baseDir + "/" + t
	}
	return path.Clean(t)
}

// activeTSPaths holds the paths configuration for the scan currently running.
// It is process-global because the LanguageParser interface (Parse(path, src))
// has no room for per-scan options; only the scan layer (FullScan /
// IncrementalScan) parses files, and it sets this once per scan. The MCP
// server never parses, so there is no concurrent reader.
var activeTSPaths struct {
	mu  sync.RWMutex
	cfg *TSPathsConfig
}

// SetActiveTSPathsConfig installs the paths configuration used by
// resolveTSImport until the next call. Passing nil restores raw-alias
// behavior (aliases stored unresolved).
func SetActiveTSPathsConfig(cfg *TSPathsConfig) {
	activeTSPaths.mu.Lock()
	defer activeTSPaths.mu.Unlock()
	activeTSPaths.cfg = cfg
}

// activeTSPathsConfig returns the current paths configuration, or nil.
func activeTSPathsConfig() *TSPathsConfig {
	activeTSPaths.mu.RLock()
	defer activeTSPaths.mu.RUnlock()
	return activeTSPaths.cfg
}

// stripJSONC removes // and /* */ comments and trailing commas from JSONC
// input, leaving string contents untouched. tsconfig files are JSONC by
// convention, so the strict encoding/json parser needs this pre-pass.
func stripJSONC(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	i := 0
	for i < len(src) {
		c := src[i]
		if inString {
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				out = append(out, src[i+1])
				i += 2
				continue
			}
			if c == '"' {
				inString = false
			}
			i++
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
		case c == ',':
			// Drop the comma when the next non-whitespace byte closes an
			// object or array (JSONC trailing comma).
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				i++
				continue
			}
			out = append(out, c)
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}
