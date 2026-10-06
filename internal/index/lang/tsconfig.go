package lang

import (
	"path"
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
// root and returns its paths configuration, following relative extends, or
// nil when neither file exists, parsing fails, or no paths are configured.
// JSONC syntax (comments, trailing commas) is tolerated. Scans use the
// nearest config per directory instead; see tsProject.
func LoadTSPathsConfig(root string) *TSPathsConfig {
	return loadTSConfigIn(root, ".")
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

// activeTS holds the TypeScript resolution state for the scan currently
// running. It is process-global because the LanguageParser interface
// (Parse(path, src)) has no room for per-scan options; only the scan layer
// (FullScan / IncrementalScan) parses files, and it installs this once per
// scan through BeginScan. Parsing runs on a worker pool, so the project's
// caches are locked.
//
// cfg, when set, is a single configuration used for every file; tests use it
// through SetActiveTSPathsConfig. Otherwise project resolves per directory.
var activeTS struct {
	mu      sync.RWMutex
	cfg     *TSPathsConfig
	project *tsProject
	refs    int // TypeScript, Vue, Svelte and Astro all begin a scan; they share one project
}

// SetActiveTSPathsConfig installs one paths configuration for every file
// until the next call. Passing nil restores per-directory resolution, or
// raw-alias behavior outside a scan.
func SetActiveTSPathsConfig(cfg *TSPathsConfig) {
	activeTS.mu.Lock()
	defer activeTS.mu.Unlock()
	activeTS.cfg = cfg
}

func activeTSState() (*TSPathsConfig, *tsProject) {
	activeTS.mu.RLock()
	defer activeTS.mu.RUnlock()
	return activeTS.cfg, activeTS.project
}

// resolveTSAlias resolves a non-relative specifier for a file: through
// tsconfig paths first (the nearest config that defines them), then as a
// workspace package. ok is false when neither applies, and the specifier is
// then an external package.
func resolveTSAlias(importerFile, spec string) (string, bool) {
	cfg, project := activeTSState()
	if cfg != nil {
		return cfg.Resolve(spec)
	}
	if project == nil {
		return "", false
	}
	if c := project.configFor(path.Dir(importerFile)); c != nil {
		if r, ok := c.Resolve(spec); ok {
			return r, true
		}
	}
	return project.resolvePackage(spec)
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
			for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
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

// beginTSScan installs the repository's TypeScript project (per-directory
// tsconfig paths and workspace packages) for the duration of a scan.
//
// The state is process-global because Parse has no per-scan argument; see the
// comment on activeTS. Routing it through ScanHook is what keeps that detail
// inside this package instead of requiring an edit to every scan entry point.
func beginTSScan(root string) func() {
	activeTS.mu.Lock()
	if activeTS.project == nil || activeTS.project.root != root {
		activeTS.mu.Unlock()
		// Built outside the lock: discovery walks the repository.
		project := newTSProject(root)
		activeTS.mu.Lock()
		if activeTS.project == nil || activeTS.project.root != root {
			activeTS.project, activeTS.refs = project, 0
		}
	}
	activeTS.refs++
	project := activeTS.project
	activeTS.mu.Unlock()

	return func() {
		activeTS.mu.Lock()
		defer activeTS.mu.Unlock()
		if activeTS.project != project {
			return
		}
		if activeTS.refs--; activeTS.refs <= 0 {
			activeTS.project, activeTS.refs = nil, 0
		}
	}
}
