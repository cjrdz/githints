package lang

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cjrdz/githints/internal/safefs"
)

// A TypeScript monorepo has many tsconfig.json files and many packages, and
// the import resolution used to know about one of each: the root tsconfig's
// paths, and nothing about workspace packages. tsProject is the per-scan view
// of the whole repository: the nearest tsconfig (following relative extends)
// for each directory, and every package.json name, so `@acme/ui` resolves to
// the package's source the way the bundler would.

// Limits on discovery. A repository is untrusted input and this runs in the
// post-commit hook, so the walk is bounded in depth, count and file size.
const (
	maxTSExtendsDepth   = 8
	maxWorkspaceDepth   = 8
	maxWorkspacePackage = 5000
)

// maxProjectFileBytes caps project files the index reads for configuration
// (go.mod, tsconfig.json, package.json). They come from the repository, which
// may be a hostile clone, and a link to /dev/zero must not hang the hook.
const maxProjectFileBytes = 4 << 20

// tsSourceExts are the files a workspace import can land on, in the order a
// bundler would usually prefer them.
var tsSourceExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte", ".astro"}

// workspacePackage is one package.json with a name.
type workspacePackage struct {
	dir     string   // repo-relative
	entries []string // candidate entry files from package.json, repo-relative
	exports map[string]string
}

type tsProject struct {
	root     string
	mu       sync.Mutex
	configs  map[string]*TSPathsConfig // dir -> nearest config that defines paths (nil: none)
	packages map[string]workspacePackage
	names    []string // package names, longest first
}

func newTSProject(root string) *tsProject {
	p := &tsProject{root: root, configs: map[string]*TSPathsConfig{}, packages: map[string]workspacePackage{}}
	p.discoverPackages()
	return p
}

// configFor returns the paths configuration that applies to files in
// repo-relative dir: the nearest tsconfig.json or jsconfig.json, at or above
// dir, whose paths (own or inherited through extends) are not empty.
//
// A nearer tsconfig without paths does not hide a farther one that has them.
// Nested tsconfigs that exist only to adjust, say, test settings are common,
// and treating them as authoritative would drop aliases the root config
// provides for those files.
func (p *tsProject) configFor(dir string) *TSPathsConfig {
	dir = path.Clean(dir)
	p.mu.Lock()
	if c, ok := p.configs[dir]; ok {
		p.mu.Unlock()
		return c
	}
	p.mu.Unlock()

	c := loadTSConfigIn(p.root, dir)
	if c == nil && dir != "." {
		c = p.configFor(path.Dir(dir))
	}

	p.mu.Lock()
	p.configs[dir] = c
	p.mu.Unlock()
	return c
}

// loadTSConfigIn reads tsconfig.json (or jsconfig.json) in repo-relative dir
// and returns its effective paths configuration, or nil when there is no such
// file or it defines no paths.
func loadTSConfigIn(root, dir string) *TSPathsConfig {
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		rel := path.Join(dir, name)
		if !fileExists(root, rel) {
			continue
		}
		eff, ok := readTSConfigChain(root, rel, map[string]bool{}, 0)
		if !ok || len(eff.paths) == 0 {
			return nil
		}
		base := eff.pathsDir
		if eff.hasBase {
			base = eff.baseDir
		}
		if base == "." {
			base = ""
		}
		return &TSPathsConfig{baseDir: base, paths: eff.paths}
	}
	return nil
}

// tsEffective is a tsconfig's compilerOptions after extends is applied.
type tsEffective struct {
	baseDir  string // repo-relative, when baseUrl is set somewhere in the chain
	hasBase  bool
	paths    map[string][]string
	pathsDir string // the directory of the config that defined paths
}

type tsconfigFile struct {
	Extends         json.RawMessage `json:"extends"`
	CompilerOptions struct {
		BaseURL *string             `json:"baseUrl"`
		Paths   map[string][]string `json:"paths"`
	} `json:"compilerOptions"`
}

// readTSConfigChain applies a tsconfig's extends chain, base first. TypeScript
// resolves baseUrl against the config that sets it, and paths targets against
// baseUrl or, without one, against the config that sets paths. Only relative
// extends are followed; a package name (e.g. "@tsconfig/node20") lives in
// node_modules, which is not part of the repository.
func readTSConfigChain(root, rel string, seen map[string]bool, depth int) (tsEffective, bool) {
	if depth > maxTSExtendsDepth || seen[rel] {
		return tsEffective{}, false
	}
	seen[rel] = true
	data, err := safefs.ReadRepoFile(root, filepath.FromSlash(rel), maxProjectFileBytes)
	if err != nil {
		return tsEffective{}, false
	}
	var cfg tsconfigFile
	if err := json.Unmarshal(stripJSONC(data), &cfg); err != nil {
		return tsEffective{}, false
	}
	dir := path.Dir(rel)

	var eff tsEffective
	for _, parent := range extendsList(cfg.Extends) {
		if !strings.HasPrefix(parent, ".") {
			continue
		}
		prel := path.Join(dir, parent)
		if !strings.HasSuffix(prel, ".json") {
			if fileExists(root, prel+".json") {
				prel += ".json"
			} else {
				prel = path.Join(prel, "tsconfig.json")
			}
		}
		if !filepath.IsLocal(filepath.FromSlash(prel)) {
			continue // outside the repository
		}
		if pe, ok := readTSConfigChain(root, prel, seen, depth+1); ok {
			// With several bases (TS 5 arrays), later ones win per field.
			if pe.hasBase {
				eff.baseDir, eff.hasBase = pe.baseDir, true
			}
			if pe.paths != nil {
				eff.paths, eff.pathsDir = pe.paths, pe.pathsDir
			}
		}
	}

	if b := cfg.CompilerOptions.BaseURL; b != nil {
		bd := path.Join(dir, *b)
		if filepath.IsLocal(filepath.FromSlash(bd)) || bd == "." {
			eff.baseDir, eff.hasBase = bd, true
		}
	}
	if cfg.CompilerOptions.Paths != nil {
		eff.paths, eff.pathsDir = cfg.CompilerOptions.Paths, dir
	}
	return eff, true
}

// extendsList accepts extends as a string or (TypeScript 5) an array.
func extendsList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

// fileExists reports whether repo-relative rel is a regular file inside root.
// Lstat: a link is not followed, so it cannot point resolution outside.
func fileExists(root, rel string) bool {
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return false
	}
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && fi.Mode().IsRegular()
}

// skipWorkspaceDir names directories that never hold workspace packages of
// the repository itself.
func skipWorkspaceDir(name string) bool {
	switch name {
	case "node_modules", "dist", "build", "out", "coverage", "vendor", "bower_components":
		return true
	}
	return strings.HasPrefix(name, ".")
}

type packageJSON struct {
	Name    string          `json:"name"`
	Source  string          `json:"source"`
	Types   string          `json:"types"`
	Typings string          `json:"typings"`
	Module  string          `json:"module"`
	Main    string          `json:"main"`
	Exports json.RawMessage `json:"exports"`
}

// discoverPackages records every named package.json in the repository.
func (p *tsProject) discoverPackages() {
	count := 0
	_ = filepath.WalkDir(p.root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable directories are skipped, not fatal
		}
		rel, rerr := filepath.Rel(p.root, abs)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (skipWorkspaceDir(d.Name()) || strings.Count(rel, "/") >= maxWorkspaceDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "package.json" || !d.Type().IsRegular() {
			return nil
		}
		if count++; count > maxWorkspacePackage {
			return filepath.SkipAll
		}
		data, err := safefs.ReadRepoFile(p.root, filepath.FromSlash(rel), maxProjectFileBytes)
		if err != nil {
			return nil
		}
		var pj packageJSON
		if json.Unmarshal(data, &pj) != nil || pj.Name == "" {
			return nil
		}
		dir := path.Dir(rel)
		pkg := workspacePackage{dir: dir, exports: exportsMap(pj.Exports)}
		for _, e := range []string{pj.Source, pj.Types, pj.Typings, pj.Module, pj.Main, pkg.exports["."]} {
			if e != "" && !strings.HasSuffix(e, ".d.ts") {
				pkg.entries = append(pkg.entries, path.Join(dir, e))
			}
		}
		if _, dup := p.packages[pj.Name]; !dup {
			p.packages[pj.Name] = pkg
			p.names = append(p.names, pj.Name)
		}
		return nil
	})
	sort.Slice(p.names, func(i, j int) bool {
		if len(p.names[i]) != len(p.names[j]) {
			return len(p.names[i]) > len(p.names[j])
		}
		return p.names[i] < p.names[j]
	})
}

// exportsMap flattens the common shapes of package.json "exports" into
// subpath -> file: a string ("."), or an object of subpaths whose values are
// strings or condition objects (types/import/default/require, first found).
func exportsMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		out["."] = s
		return out
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	pick := func(v json.RawMessage) string {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		var cond map[string]json.RawMessage
		if json.Unmarshal(v, &cond) != nil {
			return ""
		}
		for _, k := range []string{"source", "development", "import", "default", "require", "types"} {
			var s string
			if c, ok := cond[k]; ok && json.Unmarshal(c, &s) == nil && !strings.HasSuffix(s, ".d.ts") {
				return s
			}
		}
		return ""
	}
	isConditions := true
	for k := range m {
		if strings.HasPrefix(k, ".") {
			isConditions = false
			break
		}
	}
	if isConditions {
		out["."] = pick(raw)
		return out
	}
	for k, v := range m {
		if f := pick(v); f != "" {
			out[k] = f
		}
	}
	return out
}

// resolvePackage maps a workspace package import to a source file in the
// repository: "@acme/ui" to its entry, "@acme/ui/button" to the subpath. It
// only answers with a file that exists, so a package whose entry points at
// build output with no source counterpart stays unresolved rather than
// becoming an edge to nothing.
func (p *tsProject) resolvePackage(spec string) (string, bool) {
	for _, name := range p.names {
		var sub string
		switch {
		case spec == name:
		case strings.HasPrefix(spec, name+"/"):
			sub = spec[len(name)+1:]
		default:
			continue
		}
		pkg := p.packages[name]
		var candidates []string
		if sub == "" {
			candidates = append(candidates, pkg.entries...)
			candidates = append(candidates, path.Join(pkg.dir, "src", "index"), path.Join(pkg.dir, "index"))
		} else {
			if e, ok := pkg.exports["./"+sub]; ok {
				candidates = append(candidates, path.Join(pkg.dir, e))
			}
			candidates = append(candidates, path.Join(pkg.dir, "src", sub), path.Join(pkg.dir, sub))
		}
		for _, c := range candidates {
			if f, ok := p.existingSource(c); ok {
				return f, true
			}
		}
		return "", false
	}
	return "", false
}

// existingSource finds the file a bundler would load for candidate: the path
// itself, the path with a source extension, or its index file. A dist/ path
// is also tried with src/ in its place, since packages often point main at
// build output that is not in the repository.
func (p *tsProject) existingSource(candidate string) (string, bool) {
	tries := []string{candidate}
	if strings.Contains(candidate, "/dist/") {
		tries = append(tries, strings.Replace(candidate, "/dist/", "/src/", 1))
	}
	for _, c := range tries {
		stem := c
		for _, ext := range tsSourceExts {
			if strings.HasSuffix(stem, ext) {
				stem = strings.TrimSuffix(stem, ext)
				break
			}
		}
		if fileExists(p.root, c) && isTSSource(c) {
			return c, true
		}
		for _, ext := range tsSourceExts {
			if fileExists(p.root, stem+ext) {
				return stem + ext, true
			}
		}
		for _, ext := range tsSourceExts {
			if f := path.Join(stem, "index"+ext); fileExists(p.root, f) {
				return f, true
			}
		}
	}
	return "", false
}

func isTSSource(f string) bool {
	for _, ext := range tsSourceExts {
		if strings.HasSuffix(f, ext) {
			return true
		}
	}
	return false
}
