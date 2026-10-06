package lang

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// maxDeclScan bounds the forward search for a declaration's closing line. A
// declaration that large is either generated or malformed, and an unbounded
// scan turns one pathological file into a quadratic one.
const maxDeclScan = 2000

// tabWidth is how much a leading tab counts toward indentation. Mixing tabs
// and spaces is already an error in the languages that care, so the exact
// value only has to be consistent.
const tabWidth = 8

// SpecParser is a LanguageParser driven entirely by a Spec. Every language
// that does not need real parsing fidelity can be one of these.
//
// A SpecParser is immutable once built and Parse keeps its state in locals, so
// one may be shared across concurrent scans.
type SpecParser struct {
	language    string
	extensions  []string
	depthStyle  DepthStyle
	importPath  ImportPathStyle
	indexNames  []string
	stripPrefix []string
	rootMarkers []string
	rootSubdirs []string
	blanker     *Blanker
	symbols     []compiledRule
	imports     []compiledRule
}

// SpecParser must satisfy the same interface a hand-written parser does; the
// registry cannot tell them apart, and should not.
var _ LanguageParser = (*SpecParser)(nil)

// compiledRule is a spec rule with its pattern compiled and the capture groups
// resolved to indices, so matching costs no map lookups.
type compiledRule struct {
	kind     SymbolKind
	requires string
	re       *regexp.Regexp
	valueIdx int // the group the caller reads: name for symbols, path for imports
	sigIdx   int // -1 when the rule captures no signature
}

// NewSpecParser compiles a spec into a parser. The spec is validated first, so
// a parser either works or does not exist.
func NewSpecParser(spec Spec) (*SpecParser, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	p := &SpecParser{
		language:    spec.Language,
		extensions:  append([]string(nil), spec.Extensions...),
		depthStyle:  spec.DepthStyle,
		importPath:  spec.ImportPath,
		indexNames:  append([]string(nil), spec.ImportPathIndexNames...),
		stripPrefix: append([]string(nil), spec.ImportPathStripPrefixes...),
		rootMarkers: append([]string(nil), spec.ImportPathRootMarkers...),
		rootSubdirs: append([]string(nil), spec.ImportPathRootSubdirs...),
		blanker:     NewBlanker(spec.BlankSpec()),
	}
	var err error
	if p.symbols, err = compileRules(spec.Symbols, "name"); err != nil {
		return nil, fmt.Errorf("language %s: %w", spec.Language, err)
	}
	if p.imports, err = compileRules(spec.Imports, "path"); err != nil {
		return nil, fmt.Errorf("language %s: %w", spec.Language, err)
	}
	return p, nil
}

func compileRules(rules []SpecRule, group string) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rules))
	for _, rule := range rules {
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", rule.Pattern, err)
		}
		out = append(out, compiledRule{
			kind:     rule.Kind,
			requires: rule.Requires,
			re:       re,
			valueIdx: re.SubexpIndex(group),
			sigIdx:   re.SubexpIndex("sig"),
		})
	}
	return out, nil
}

func (p *SpecParser) Language() string     { return p.language }
func (p *SpecParser) Extensions() []string { return p.extensions }

// Parse blanks the source, then runs each rule over the lines that survive.
//
// Declarations inside a function body are skipped. A local helper is not part
// of a file's interface, and indexing every closure would bury the symbols a
// reader is actually looking for. Declarations nested in a class, module or
// namespace are kept, which is what makes this usable for languages where
// nothing lives at top level.
func (p *SpecParser) Parse(path string, src []byte) ([]Symbol, []Import, error) {
	lines := p.blanker.Blank(src)
	depths := p.lineDepths(lines)

	// Imports are matched against a view that keeps string contents: an import
	// path is almost always inside a string literal, and the fully blanked
	// view would show an empty one. Comments are still stripped there, so a
	// commented-out import is not indexed as a real dependency.
	importLines := lines
	if len(p.imports) > 0 {
		importLines = p.blanker.BlankKeepingStrings(src)
	}

	var symbols []Symbol
	var imports []Import
	// One file referencing the same target twice is still one dependency.
	// Most languages cannot repeat an import, but a schema can: two foreign
	// keys to the same table are two lines and one edge, and counting both
	// would inflate that table's standing in the hub ranking.
	seenImport := make(map[string]bool)

	// funcOpen holds the depth of each enclosing function body. A line is
	// inside a function when anything is on the stack.
	var funcOpen []int

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		depth := depths[i]

		for len(funcOpen) > 0 && depth <= funcOpen[len(funcOpen)-1] {
			funcOpen = funcOpen[:len(funcOpen)-1]
		}
		inFunction := len(funcOpen) > 0

		for _, rule := range p.imports {
			if m := rule.match(importLines[i]); m != nil {
				v := m[rule.valueIdx]
				if strings.HasPrefix(v, ".") && p.importPath == ImportPathDotted {
					v = p.resolveRelativeImport(path, v)
				}
				if v != "" && !seenImport[v] {
					seenImport[v] = true
					imports = append(imports, Import{FilePath: path, ImportedPath: v})
				}
			}
		}

		if inFunction {
			continue
		}

		// A declaration whose parameter list spans lines is invisible to a
		// pattern that expects a closing paren, which is one of the larger
		// systematic gaps in spec-driven parsing. Joining the continuation
		// lines first lets one pattern cover both spellings.
		logical, lastLine := joinContinuation(lines, i)

		for _, rule := range p.symbols {
			m := rule.match(logical)
			if m == nil {
				continue
			}
			name := m[rule.valueIdx]
			if name == "" {
				continue
			}
			sym := Symbol{
				Name:      name,
				Kind:      rule.kind,
				FilePath:  path,
				LineStart: i + 1,
				LineEnd:   p.declEnd(lines, depths, i, lastLine),
			}
			if rule.sigIdx >= 0 {
				sym.Signature = strings.TrimSpace(m[rule.sigIdx])
			}
			symbols = append(symbols, sym)

			if rule.kind == KindFunc || rule.kind == KindMethod {
				funcOpen = append(funcOpen, depth)
			}
			break // one symbol per line; the first matching rule wins
		}
	}
	return symbols, imports, nil
}

// match applies a rule's gate before its pattern. The gate is the reason a
// spec-driven scan can afford many rules across many languages.
func (r compiledRule) match(line string) []string {
	if r.requires != "" && !strings.Contains(line, r.requires) {
		return nil
	}
	return r.re.FindStringSubmatch(line)
}

// lineDepths returns the nesting depth at the start of each line.
//
// For brace languages that is the running count of unclosed braces, which is
// only meaningful because blanking already removed braces in strings and
// comments. For indent languages it is the line's own indentation. Languages
// with neither report zero everywhere, so nothing is ever treated as nested.
func (p *SpecParser) lineDepths(lines []string) []int {
	depths := make([]int, len(lines))
	switch p.depthStyle {
	case DepthBrace:
		depth := 0
		for i, line := range lines {
			depths[i] = depth
			depth += braceDelta(line)
			if depth < 0 {
				depth = 0
			}
		}
	case DepthIndent:
		for i, line := range lines {
			depths[i] = indentWidth(line)
		}
	}
	return depths
}

// declEnd finds the last line of the declaration starting at index start.
//
// header is the last line of the declaration's own header, which differs from
// start when a parameter list spans lines: the opening brace is then on the
// last header line, not the first.
func (p *SpecParser) declEnd(lines []string, depths []int, start, header int) int {
	limit := start + maxDeclScan
	if limit > len(lines)-1 {
		limit = len(lines) - 1
	}

	switch p.depthStyle {
	case DepthBrace:
		// Walk until the braces opened across the header are balanced.
		depth := depths[start]
		for i := start; i <= header && i < len(lines); i++ {
			depth += braceDelta(lines[i])
		}
		if depth <= depths[start] {
			return header + 1 // the declaration ends with its header
		}
		for i := header + 1; i <= limit; i++ {
			depth += braceDelta(lines[i])
			if depth <= depths[start] {
				return i + 1
			}
		}
	case DepthIndent:
		// The body is everything indented further than the declaration.
		end := header
		for i := header + 1; i <= limit; i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			if indentWidth(lines[i]) <= depths[start] {
				break
			}
			end = i
		}
		return end + 1
	}
	return header + 1
}

func braceDelta(line string) int {
	delta := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '{':
			delta++
		case '}':
			delta--
		}
	}
	return delta
}

func indentWidth(line string) int {
	width := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			width++
		case '\t':
			width += tabWidth
		default:
			return width
		}
	}
	return width
}

// ImportPath maps a file to the key importers name it by, per the spec's
// import_path style. A spec that declares none does not implement the
// behaviour at all, so the registry reports the language as not resolving
// import paths rather than inventing a key that nothing would match.
func (p *SpecParser) ImportPath(root, file string) (string, error) {
	if p.importPath == ImportPathNone {
		return "", fmt.Errorf("language %s does not resolve import paths", p.language)
	}

	key := p.sourceRelative(root, filepath.ToSlash(file))
	if ext := path.Ext(key); ext != "" {
		key = strings.TrimSuffix(key, ext)
	}
	// A directory's index file is imported as the directory.
	for _, name := range p.indexNames {
		if key == name {
			return "", fmt.Errorf("%s at the repository root has no import path", name)
		}
		if strings.HasSuffix(key, "/"+name) {
			key = strings.TrimSuffix(key, "/"+name)
			break
		}
	}
	if key == "" {
		return "", fmt.Errorf("no import path for %s", file)
	}
	if p.importPath == ImportPathDotted {
		key = strings.ReplaceAll(key, "/", ".")
	}
	return key, nil
}

// sourceRelative strips the part of a repository path that is not part of
// the import path.
//
// A strip prefix (Java's src/main/java) is removed wherever it sits, not
// only at the repository root: in a multi-module build every module has its
// own. Failing that, a project root marker (Python's pyproject.toml) makes
// the path relative to the nearest marked directory, and to its src/ when
// the file is under one.
func (p *SpecParser) sourceRelative(root, key string) string {
	for _, prefix := range p.stripPrefix {
		prefix = strings.Trim(filepath.ToSlash(prefix), "/")
		if strings.HasPrefix(key, prefix+"/") {
			return strings.TrimPrefix(key, prefix+"/")
		}
		if i := strings.Index(key, "/"+prefix+"/"); i >= 0 {
			return key[i+len(prefix)+2:]
		}
	}
	if len(p.rootMarkers) == 0 || root == "" {
		return key
	}
	project, ok := p.projectDir(root, path.Dir(key))
	if !ok {
		return key
	}
	rel := key
	if project != "." {
		rel = strings.TrimPrefix(key, project+"/")
	}
	for _, sub := range p.rootSubdirs {
		sub = strings.Trim(filepath.ToSlash(sub), "/")
		if strings.HasPrefix(rel, sub+"/") {
			return strings.TrimPrefix(rel, sub+"/")
		}
	}
	return rel
}

// projectDir returns the nearest directory at or above dir that holds one of
// the root markers.
func (p *SpecParser) projectDir(root, dir string) (string, bool) {
	for {
		for _, m := range p.rootMarkers {
			if fileExists(root, path.Join(dir, m)) {
				return dir, true
			}
		}
		if dir == "." || dir == "/" || dir == "" {
			return "", false
		}
		dir = path.Dir(dir)
	}
}

// resolveRelativeImport turns a relative dotted import (Python's
// "from .models import X", ".models"; "from .. import y", "..") into the
// absolute module path, using the importing file's own import path. That
// needs the repository root, so outside a scan -- or when the file has no
// import path, or the import climbs above its project -- the specifier is
// kept as written: it still shows in the file's note, it just links nowhere.
func (p *SpecParser) resolveRelativeImport(file, rel string) string {
	root := activeSpecRoot()
	if root == "" {
		return rel
	}
	key, err := p.ImportPath(root, file)
	if err != nil {
		return rel
	}
	// The package a module lives in: the module itself for an index file
	// (__init__), its parent otherwise.
	pkg := key
	stem := strings.TrimSuffix(path.Base(filepath.ToSlash(file)), path.Ext(file))
	isIndex := false
	for _, n := range p.indexNames {
		if stem == n {
			isIndex = true
		}
	}
	parts := strings.Split(pkg, ".")
	if !isIndex {
		parts = parts[:len(parts)-1]
	}
	dots := len(rel) - len(strings.TrimLeft(rel, "."))
	up := dots - 1
	if up > len(parts) {
		return rel // climbs above the project
	}
	parts = parts[:len(parts)-up]
	if rest := strings.TrimLeft(rel, "."); rest != "" {
		parts = append(parts, rest)
	}
	return strings.Join(parts, ".")
}

// BeginScan installs the repository root that relative imports resolve
// against for the duration of a scan.
func (p *SpecParser) BeginScan(root string) func() {
	if p.importPath != ImportPathDotted {
		return nil
	}
	return beginSpecScan(root)
}

// activeSpec holds the root for the scan in progress; Parse has no root
// parameter. Every dotted spec language begins a scan, so it is counted.
var activeSpec struct {
	mu   sync.Mutex
	root string
	refs int
}

func beginSpecScan(root string) func() {
	activeSpec.mu.Lock()
	if activeSpec.root != root {
		activeSpec.root, activeSpec.refs = root, 0
	}
	activeSpec.refs++
	activeSpec.mu.Unlock()
	return func() {
		activeSpec.mu.Lock()
		defer activeSpec.mu.Unlock()
		if activeSpec.root != root {
			return
		}
		if activeSpec.refs--; activeSpec.refs <= 0 {
			activeSpec.root, activeSpec.refs = "", 0
		}
	}
}

func activeSpecRoot() string {
	activeSpec.mu.Lock()
	defer activeSpec.mu.Unlock()
	return activeSpec.root
}

// BlankLines returns the comment- and string-free view of the file.
func (p *SpecParser) BlankLines(src []byte) []string { return p.blanker.Blank(src) }

// BlankLinesKeepingStrings keeps string contents, which detectors need.
func (p *SpecParser) BlankLinesKeepingStrings(src []byte) []string {
	return p.blanker.BlankKeepingStrings(src)
}

// maxContinuationJoin bounds how far a declaration header may span. Real
// signatures are a handful of lines; a larger span means the parentheses were
// never balanced, and joining further would drag unrelated code into the match.
const maxContinuationJoin = 24

// joinContinuation returns the logical declaration line starting at i, and the
// index of its last physical line.
//
// A line with unbalanced parentheses is a declaration whose parameter list
// continues, so the following lines are folded in until the parentheses close.
// A balanced line is returned untouched, which is the overwhelmingly common
// case and costs nothing.
func joinContinuation(lines []string, i int) (string, int) {
	if parenDelta(lines[i]) <= 0 {
		return lines[i], i
	}

	var b strings.Builder
	b.WriteString(lines[i])
	depth := parenDelta(lines[i])

	limit := i + maxContinuationJoin
	if limit > len(lines)-1 {
		limit = len(lines) - 1
	}
	for j := i + 1; j <= limit; j++ {
		// A single space stands in for the newline so tokens cannot merge
		// across the join: "foo(\n a" must not read as "foo( a" glued to the
		// previous token.
		b.WriteByte(' ')
		b.WriteString(strings.TrimSpace(lines[j]))
		depth += parenDelta(lines[j])
		if depth <= 0 {
			return b.String(), j
		}
	}
	// Never closed: treat the line as it stands rather than returning a blob.
	return lines[i], i
}

func parenDelta(line string) int {
	delta := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '(':
			delta++
		case ')':
			delta--
		}
	}
	return delta
}
