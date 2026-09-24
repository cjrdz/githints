// Package lang contains the language-agnostic structural-index types and the
// registry of supported language parsers. It is a sub-package of index so that
// individual language parsers can be added as one new file under lang/ without
// touching the storage or rendering packages.
package lang

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SymbolKind classifies a symbol. For Go it mirrors the coarse categories an
// agent needs when orienting itself to a file: func, method, type, const, var.
type SymbolKind string

const (
	KindFunc   SymbolKind = "func"
	KindMethod SymbolKind = "method"
	KindType   SymbolKind = "type"
	KindConst  SymbolKind = "const"
	KindVar    SymbolKind = "var"
)

// Symbol is one named definition in a source file.
type Symbol struct {
	Name      string
	Kind      SymbolKind
	FilePath  string
	LineStart int
	LineEnd   int
	Signature string // optional, e.g. "func (r *Receiver) MethodName(p Type) Return"

	// Language is stamped by the scan layer from the parser that produced the
	// symbol, rather than by each parser, so no parser can forget to set it.
	Language string
}

// Import is one import statement in a source file. The ImportedPath is the
// import path as it appears in the source (e.g. "fmt" or "github.com/x/y").
type Import struct {
	FilePath     string
	ImportedPath string
}

// LanguageParser turns a source file into symbols and imports.
type LanguageParser interface {
	// Language returns the canonical name used in config and meta reporting.
	Language() string

	// Extensions returns the file extensions this parser accepts, including
	// the leading dot and lower-case only.
	Extensions() []string

	// Parse returns symbols and imports for one file. The parser must be
	// deterministic and safe on malformed input: it returns an error only when
	// parsing itself failed in an unrecoverable way; individual malformed nodes
	// should be skipped.
	Parse(path string, src []byte) ([]Symbol, []Import, error)
}

// Registry is a set of parsers keyed by language name.
type Registry struct {
	parsers map[string]LanguageParser
	byExt   map[string]LanguageParser
	// origin records where each language came from, so the CLI can show a
	// user which of their languages the repository supplied.
	origin map[string]string
}

// Origins reported by Registry.Origin.
const (
	OriginBuiltin = "built-in"
	OriginRepo    = "repo"
)

// NewRegistry creates a registry pre-loaded with all supported parsers.
func NewRegistry() *Registry {
	r := &Registry{
		parsers: make(map[string]LanguageParser),
		byExt:   make(map[string]LanguageParser),
		origin:  make(map[string]string),
	}
	r.register(GoParser{})
	r.register(TypeScriptParser{})
	r.register(SvelteParser{})
	r.register(AstroParser{})

	// Languages shipped as data. These register after the native parsers, so
	// a spec cannot displace a hand-written parser -- it is reported as a
	// clash and skipped instead.
	parsers, errs := EmbeddedParsers()
	for _, err := range errs {
		warnf("built-in language spec ignored: %v", err)
	}
	for _, p := range parsers {
		if err := r.registerSpecParser(p, OriginBuiltin); err != nil {
			warnf("built-in language %s ignored: %v", p.Language(), err)
		}
	}
	return r
}

// NewRegistryForRoot is NewRegistry plus any language specs the repository
// ships in .githints/langs.
//
// Repository specs register last, so they can extend the set but never
// displace a language the binary already provides: a clash is reported and the
// repository's spec skipped. That keeps `go` meaning the same thing in every
// checkout.
func NewRegistryForRoot(root string) *Registry {
	r := NewRegistry()
	if root == "" {
		return r
	}
	parsers, errs := LoadUserSpecs(root)
	for _, err := range errs {
		warnf("repository language spec ignored: %v", err)
	}
	for _, p := range parsers {
		if err := r.registerSpecParser(p, OriginRepo); err != nil {
			warnf("repository language %s ignored: %v", p.Language(), err)
		}
	}
	return r
}

// Origin reports where a language came from: OriginBuiltin or OriginRepo.
func (r *Registry) Origin(name string) string {
	return r.origin[strings.ToLower(name)]
}

// register adds a parser to the registry. It panics if two parsers claim the same
// extension or the same language name, which is a build-time bug.
func (r *Registry) register(p LanguageParser) {
	name := strings.ToLower(p.Language())
	if _, ok := r.parsers[name]; ok {
		panic(fmt.Sprintf("duplicate language parser: %s", name))
	}
	r.parsers[name] = p
	r.origin[name] = OriginBuiltin
	for _, ext := range p.Extensions() {
		e := strings.ToLower(ext)
		if existing, ok := r.byExt[e]; ok {
			panic(fmt.Sprintf("extension %q claimed by %s and %s", e, existing.Language(), p.Language()))
		}
		r.byExt[e] = p
	}
}

// ForLanguage returns the parser for a language name, or nil if none.
func (r *Registry) ForLanguage(name string) LanguageParser {
	return r.parsers[strings.ToLower(name)]
}

// ForPath returns the parser for a file path, or nil if none.
func (r *Registry) ForPath(path string) LanguageParser {
	ext := strings.ToLower(filepath.Ext(path))
	return r.byExt[ext]
}

// AllParsers returns every registered parser.
func (r *Registry) AllParsers() []LanguageParser {
	out := make([]LanguageParser, 0, len(r.parsers))
	for _, p := range r.parsers {
		out = append(out, p)
	}
	return out
}

// Languages reports every supported language name, sorted. The order is
// deterministic because it reaches users directly: `githints index languages`
// prints it, and ResolveLanguages puts it in an error message.
func (r *Registry) Languages() []string {
	out := make([]string, 0, len(r.parsers))
	for name := range r.parsers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ResolveLanguages validates a list of configured languages against the
// registry and returns a parser slice in the same order. It returns an error
// if any language is unsupported.
//
// The error names the supported set. Without it a user who configured a
// language this binary does not have gets told only that their choice is
// wrong, with nothing in the CLI to tell them what would be right.
//
// Use this for work the user invoked directly, where stopping with a clear
// error is the helpful answer. Unattended callers should use
// ResolveKnownLanguages instead.
func (r *Registry) ResolveLanguages(names []string) ([]LanguageParser, error) {
	parsers, unknown, err := r.ResolveKnownLanguages(names)
	if err != nil {
		return nil, err
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unsupported index language: %q (supported: %s)",
			unknown[0], strings.Join(r.Languages(), ", "))
	}
	return parsers, nil
}

// ResolveKnownLanguages returns parsers for every name it recognizes and
// reports the rest, rather than failing on the first unknown one. It errors
// only when nothing was recognized, because a scan with no parsers would index
// nothing while reporting success.
//
// This exists for callers that run unattended. config.json travels in clones,
// so a repo naming a language that some teammate's older binary does not have
// would otherwise stop that teammate's index dead: the post-commit hook only
// warns on a scan error, so the commit succeeds and the index silently never
// updates again. Skipping the unknown name and indexing the rest keeps the
// failure visible without making it fatal.
func (r *Registry) ResolveKnownLanguages(names []string) ([]LanguageParser, []string, error) {
	parsers := make([]LanguageParser, 0, len(names))
	var unknown []string
	for _, name := range names {
		if p := r.ForLanguage(name); p != nil {
			parsers = append(parsers, p)
			continue
		}
		unknown = append(unknown, name)
	}
	if len(parsers) == 0 {
		return nil, unknown, fmt.Errorf("no supported index language configured: %q (supported: %s)",
			names, strings.Join(r.Languages(), ", "))
	}
	return parsers, unknown, nil
}

// ExtensionOf returns the lower-case extension of path, or empty string.
func ExtensionOf(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

// ParserSet deduplicates a list of parsers.
func ParserSet(parsers []LanguageParser) map[LanguageParser]struct{} {
	set := make(map[LanguageParser]struct{}, len(parsers))
	for _, p := range parsers {
		set[p] = struct{}{}
	}
	return set
}

// ExtensionMap builds a map from extension to parser. Extensions must
// already be unique within the registry.
func ExtensionMap(set map[LanguageParser]struct{}) map[string]LanguageParser {
	m := make(map[string]LanguageParser)
	for p := range set {
		for _, ext := range p.Extensions() {
			m[ext] = p
		}
	}
	return m
}

// SelectParser chooses a parser for a file from the configured set, returning
// nil if none matches the extension.
func SelectParser(extMap map[string]LanguageParser, path string) LanguageParser {
	return extMap[ExtensionOf(path)]
}

// ScanOptions bundles the per-file guard limits and rendering options.
type ScanOptions struct {
	Root         string
	Languages    []string
	MaxFileSize  int64
	ParseTimeout time.Duration
	Obsidian     bool
}

// ImportPathResolver is implemented by parsers whose language has a notion of
// the key other files import a file by: a Go module path, a TypeScript file
// key, a Python dotted module. Implementing it is what lets a language appear
// in a note's "Imported by" section, be linked from another file's "Imports",
// and rank as a hub in INDEX.md.
//
// It is the inverse of Parse: Parse produces Import.ImportedPath, and this
// turns a file back into the value importers would have written. Nothing
// enforces that the two agree, so a language that implements one should be
// tested against the other.
type ImportPathResolver interface {
	ImportPath(root, file string) (string, error)
}

// ImportPath returns the in-repo import path for a source file by asking the
// parser that owns it.
//
// This used to be a closed switch over ".go" and the TypeScript extensions,
// which meant a new language could not participate in the dependency graph at
// all: its files indexed symbols but contributed no edges, and get_dependents
// reported an error for them.
func (r *Registry) ImportPath(root, file string) (string, error) {
	p := r.ForPath(file)
	if p == nil {
		return "", fmt.Errorf("no parser for %s", file)
	}
	resolver, ok := p.(ImportPathResolver)
	if !ok {
		return "", fmt.Errorf("language %s does not resolve import paths", p.Language())
	}
	return resolver.ImportPath(root, file)
}

// LocalImportPath resolves against the built-in languages only.
//
// Callers that can reach a repository root should prefer
// NewRegistryForRoot(root).ImportPath, which also sees languages the
// repository supplies in .githints/langs.
func LocalImportPath(root, file string) (string, error) {
	return defaultRegistry().ImportPath(root, file)
}

// defaultRegistry is the built-in-only registry, built once. LocalImportPath
// is called once per file during a render, so constructing a registry per call
// would reload and recompile every spec each time.
var (
	defaultRegistryOnce sync.Once
	defaultRegistryVal  *Registry
)

func defaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() { defaultRegistryVal = NewRegistry() })
	return defaultRegistryVal
}

// IndexMeta is metadata about the most recent scan.
type IndexMeta struct {
	LastIndexedAt    int64
	FileCount        int
	SymbolCount      int
	LanguageCounts   map[string]int
	SkippedCount     int
	UnsupportedCount int
}

// IndexDBPath returns the path where index.db lives inside a repo.
func IndexDBPath(root string) string {
	return filepath.Join(root, ".githints", "index.db")
}

// IndexNotesPath returns the root of the separate index notes directory.
func IndexNotesPath(root string) string {
	return filepath.Join(root, ".githints", "index")
}

// IndexRollupPath returns the path for the root index rollup.
func IndexRollupPath(root string) string {
	return filepath.Join(root, ".githints", "INDEX.md")
}

// IsIndexPath reports whether p is a path inside the index notes directory.
func IsIndexPath(root, p string) bool {
	notes := IndexNotesPath(root)
	rel, err := filepath.Rel(notes, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, "..") && rel != "."
}

// IndexNotePath returns the path of the index note for a repo-relative source
// path, and a boolean reporting whether the path would collide with the index
// notes directory itself (i.e. the source path starts with "index/" or is "index").
//
// A collision is a serious error because the index note path would overlap with
// a hint file under .githints/index/whatever.go.md, which is integrity-verified.
func IndexNotePath(root, srcPath string) (string, bool, error) {
	if srcPath == "" {
		return "", false, fmt.Errorf("source path is empty")
	}
	if srcPath == "." {
		return "", false, fmt.Errorf("source path is '.'")
	}
	if filepath.IsAbs(srcPath) || !filepath.IsLocal(srcPath) {
		return "", false, fmt.Errorf("source path must be repo-relative and local, got: %s", srcPath)
	}
	if srcPath == "index" || strings.HasPrefix(filepath.ToSlash(srcPath), "index/") {
		return "", true, fmt.Errorf("index note path collides with the index notes directory: %s", srcPath)
	}
	return filepath.Join(IndexNotesPath(root), srcPath+".md"), false, nil
}

// NotePath returns the path to the index note for a repo-relative source
// path. It is a convenience alias for IndexNotePath intended for tests with
// non-colliding paths.
func NotePath(root, src string) string {
	p, _, err := IndexNotePath(root, src)
	if err != nil {
		panic(err)
	}
	return p
}

// FormatIndexedAt returns a printable string for a Unix timestamp.
func FormatIndexedAt(ts int64) string {
	if ts == 0 {
		return "never"
	}
	return fmt.Sprintf("%d", ts)
}

// SortedKeys returns the sorted keys of a map.
func SortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EscapeMarkdown is a minimal escape used for Obsidian display text in Phase 6.
// It mirrors the safe subset of the hint package's escape logic without importing
// it, keeping the index/hint boundary clean.
func EscapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
	)
	return replacer.Replace(s)
}

// FileLink returns either a markdown link or an Obsidian wikilink.
//
// In Obsidian mode the target is the note filename (src + ".md") with
// URL-encoded [, ], and | characters so the wikilink boundary stays intact.
// Pipe syntax is always used so the display text is the raw repo-relative path.
//
// New code should use NoteLink: its default-mode targets are relative to the
// linking document and include the .md suffix, so they resolve to the actual
// note file — this default-mode target resolves to nothing on disk.
func FileLink(root, src string, obsidian bool) string {
	if obsidian {
		return NoteLink("", src, src, true)
	}
	return fmt.Sprintf("[%s](%s)", src, filepath.ToSlash(src))
}

// NoteLink returns a link, labeled display, to the index note for target (a
// repo-relative source file). fromDir is the directory of the linking
// document relative to .githints/ — "index/<dir of the note's source>" for
// per-file notes, "." for the INDEX.md rollup — so the default markdown
// target resolves to the real note file (e.g. "../../cmd/main.go.md").
// Obsidian wikilinks are location-independent; fromDir is ignored there.
func NoteLink(fromDir, display, target string, obsidian bool) string {
	if obsidian {
		t := target + ".md"
		// URL-encode only the characters that break wikilink syntax.
		t = strings.ReplaceAll(t, "[", "%5B")
		t = strings.ReplaceAll(t, "]", "%5D")
		t = strings.ReplaceAll(t, "|", "%7C")
		return fmt.Sprintf("[[%s|%s]]", t, display)
	}
	rel, err := filepath.Rel(fromDir, filepath.Join("index", target+".md"))
	if err != nil {
		rel = filepath.Join("index", target+".md")
	}
	return fmt.Sprintf("[%s](%s)", display, encodeMarkdownTarget(filepath.ToSlash(rel)))
}

// encodeMarkdownTarget percent-encodes the characters that break markdown
// inline-link targets: spaces, brackets, and parentheses.
func encodeMarkdownTarget(s string) string {
	r := strings.NewReplacer(
		" ", "%20",
		"[", "%5B",
		"]", "%5D",
		"(", "%28",
		")", "%29",
	)
	return r.Replace(s)
}

// encodeLanguageCounts serializes a map to a comma-separated string.
func EncodeLanguageCounts(m map[string]int) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		if strings.Contains(k, ":") {
			return "", fmt.Errorf("language name %q contains separator ':'", k)
		}
		parts = append(parts, fmt.Sprintf("%s:%d", k, v))
	}
	return strings.Join(parts, ","), nil
}

// DecodeLanguageCounts parses a string serialized by EncodeLanguageCounts.
func DecodeLanguageCounts(s string) (map[string]int, error) {
	if s == "" {
		return nil, nil
	}
	m := make(map[string]int)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		idx := strings.Index(part, ":")
		if idx < 0 {
			return nil, fmt.Errorf("decode language_counts: missing ':' in %q", part)
		}
		lang := part[:idx]
		countStr := part[idx+1:]
		var count int
		if _, err := fmt.Sscanf(countStr, "%d", &count); err != nil {
			return nil, fmt.Errorf("decode language_counts: %w", err)
		}
		m[lang] = count
	}
	return m, nil
}

// EscapeLike escapes special characters for SQLite LIKE patterns.
func EscapeLike(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FileInDegreeSummary is returned by store queries for top imported files.
type FileInDegreeSummary struct {
	File       string
	Dependents int
}

// ScanHook is implemented by parsers that need state for the duration of a
// scan -- typically a project configuration that maps import aliases, which
// the Parse signature has no room to carry.
//
// BeginScan installs that state and returns its teardown. The scan layer calls
// it once per participating parser and defers the result, so a language can be
// added without touching the scan layer at all.
type ScanHook interface {
	BeginScan(root string) func()
}

// BeginScans runs the hook for every parser that has one and returns a single
// teardown that unwinds them in reverse.
//
// Parsers are deduplicated, so a family sharing one hook installs it once, and
// only the languages actually enabled for this scan take part -- the previous
// arrangement installed the TypeScript configuration even for a Go-only scan.
func BeginScans(parsers []LanguageParser, root string) func() {
	var teardowns []func()
	seen := make(map[LanguageParser]struct{}, len(parsers))
	for _, p := range parsers {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		if hook, ok := p.(ScanHook); ok {
			if done := hook.BeginScan(root); done != nil {
				teardowns = append(teardowns, done)
			}
		}
	}
	return func() {
		for i := len(teardowns) - 1; i >= 0; i-- {
			teardowns[i]()
		}
	}
}
