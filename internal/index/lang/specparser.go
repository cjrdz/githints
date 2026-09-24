package lang

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"
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
				if v := m[rule.valueIdx]; v != "" {
					imports = append(imports, Import{FilePath: path, ImportedPath: v})
				}
			}
		}

		if inFunction {
			continue
		}

		for _, rule := range p.symbols {
			m := rule.match(line)
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
				LineEnd:   p.declEnd(lines, depths, i),
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
func (p *SpecParser) declEnd(lines []string, depths []int, start int) int {
	limit := start + maxDeclScan
	if limit > len(lines)-1 {
		limit = len(lines) - 1
	}

	switch p.depthStyle {
	case DepthBrace:
		// Walk until the braces opened on the declaration line are balanced.
		depth := depths[start] + braceDelta(lines[start])
		if depth <= depths[start] {
			return start + 1 // single-line declaration
		}
		for i := start + 1; i <= limit; i++ {
			depth += braceDelta(lines[i])
			if depth <= depths[start] {
				return i + 1
			}
		}
	case DepthIndent:
		// The body is everything indented further than the declaration.
		end := start
		for i := start + 1; i <= limit; i++ {
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
	return start + 1
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
func (p *SpecParser) ImportPath(_, file string) (string, error) {
	if p.importPath == ImportPathNone {
		return "", fmt.Errorf("language %s does not resolve import paths", p.language)
	}

	key := filepath.ToSlash(file)
	// Source roots are not part of the import path.
	for _, prefix := range p.stripPrefix {
		prefix = strings.TrimSuffix(filepath.ToSlash(prefix), "/") + "/"
		if strings.HasPrefix(key, prefix) {
			key = strings.TrimPrefix(key, prefix)
			break
		}
	}
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

// BlankLines returns the comment- and string-free view of the file.
func (p *SpecParser) BlankLines(src []byte) []string { return p.blanker.Blank(src) }

// BlankLinesKeepingStrings keeps string contents, which detectors need.
func (p *SpecParser) BlankLinesKeepingStrings(src []byte) []string {
	return p.blanker.BlankKeepingStrings(src)
}
