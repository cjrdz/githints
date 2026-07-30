package lang

import (
	"path"
	"regexp"
	"strings"
)

// TypeScriptParser is a heuristic, stdlib-only parser for TypeScript and
// JavaScript. It is not a compiler: it trades accuracy for zero dependencies.
// It handles the declarations an agent needs for orientation (functions,
// classes, interfaces, type aliases, enums, methods, top-level const/let/var,
// arrow-function constants) and import specifiers (static, side-effect,
// export-from, dynamic import(), require()).
//
// Known limitations, by design:
//   - tsconfig path aliases (e.g. "@core/x") and package specifiers are stored
//     raw and never resolve to repo files, so get_dependents only matches
//     relative imports.
//   - Regex literals are disambiguated from division heuristically; a pathological
//     regex can desync brace matching for the rest of the line.
//   - Symbols declared inside function bodies are intentionally skipped to keep
//     test-file locals out of the index.
type TypeScriptParser struct{}

func (TypeScriptParser) Language() string { return "typescript" }

func (TypeScriptParser) Extensions() []string {
	return []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
}

func (TypeScriptParser) Parse(path string, src []byte) ([]Symbol, []Import, error) {
	return parseTypeScript(path, src, 0), extractTSImports(path, src), nil
}

// tsCodeExtensions are the extensions stripped when normalizing a TS-family
// file path to its import key. Order matters only for suffix matching.
var tsCodeExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs", ".svelte", ".astro"}

// tsFileKey normalizes a repo-relative TS-family path to the key used to match
// importers against files: the code extension is stripped, and a trailing
// "/index" is dropped so that "src/lib/foo/index.ts" and "src/lib/foo.ts"
// both answer to imports of "./foo".
func tsFileKey(p string) string {
	lower := strings.ToLower(p)
	for _, ext := range tsCodeExtensions {
		if strings.HasSuffix(lower, ext) {
			p = p[:len(p)-len(ext)]
			break
		}
	}
	if rest, ok := strings.CutSuffix(p, "/index"); ok {
		p = rest
	}
	return p
}

// resolveTSImport maps an import specifier to its index key. Relative
// specifiers are resolved against the importing file's directory and
// normalized with tsFileKey; package and alias specifiers are kept raw.
func resolveTSImport(importerFile, spec string) string {
	if !strings.HasPrefix(spec, ".") {
		return spec
	}
	joined := path.Join(path.Dir(importerFile), spec)
	return tsFileKey(joined)
}

// isTSCodeExtension reports whether ext (with leading dot, lower case) is a
// TS-family code extension. Used by LocalImportPath.
func isTSCodeExtension(ext string) bool {
	for _, e := range tsCodeExtensions {
		if e == ext {
			return true
		}
	}
	return false
}

// --- import extraction -----------------------------------------------------

var (
	reTSImportStart = regexp.MustCompile(`^\s*(?:import|export)\s+[\w{*]`)
	reTSFrom        = regexp.MustCompile(`\bfrom\s*["']([^"']+)["']`)
	reTSImportSide  = regexp.MustCompile(`^\s*import\s*["']([^"']+)["']`)
	reTSDynamic     = regexp.MustCompile(`\bimport\s*\(\s*["']([^"']+)["']`)
	reTSRequire     = regexp.MustCompile(`\brequire\s*\(\s*["']([^"']+)["']`)
)

// extractTSImports scans raw source lines for import specifiers. It tracks
// multi-line `import { ... } from "spec"` statements and deduplicates by
// resolved specifier so hub counts reflect files, not statements.
func extractTSImports(file string, src []byte) []Import {
	lines := strings.Split(string(src), "\n")
	seen := make(map[string]struct{})
	var out []Import
	add := func(spec string) {
		key := resolveTSImport(file, spec)
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, Import{FilePath: file, ImportedPath: key})
	}

	inStatement := false
	for _, raw := range lines {
		line := stripTSComments(raw)
		if !inStatement && reTSImportStart.MatchString(line) {
			inStatement = true
		}
		if inStatement {
			if m := reTSFrom.FindStringSubmatch(line); m != nil {
				add(m[1])
				inStatement = false
			} else if m := reTSImportSide.FindStringSubmatch(line); m != nil {
				add(m[1])
				inStatement = false
			} else if strings.Contains(line, ";") {
				// `import type Foo = ...;` and friends end without a from clause.
				inStatement = false
			}
		}
		for _, m := range reTSDynamic.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range reTSRequire.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
	}
	return out
}

// stripTSComments removes // and /* */ comments from a single line without a
// full lexer. It is used only for import extraction, where string contents
// matter; a `//` inside a string literal can truncate the line early, which is
// accepted as heuristic noise.
func stripTSComments(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		c := line[i]
		if c == '/' && i+1 < len(line) {
			switch line[i+1] {
			case '/':
				return b.String()
			case '*':
				end := strings.Index(line[i+2:], "*/")
				if end < 0 {
					return b.String()
				}
				i += 2 + end + 2
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// --- symbol extraction -----------------------------------------------------

var (
	reTSFunc      = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`)
	reTSClass     = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+([A-Za-z_$][\w$]*)`)
	reTSInterface = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?interface\s+([A-Za-z_$][\w$]*)`)
	reTSEnum      = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?(?:const\s+)?enum\s+([A-Za-z_$][\w$]*)`)
	reTSNamespace = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?(?:namespace|module)\s+([A-Za-z_$][\w$]*)`)
	reTSTypeAlias = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?type\s+([A-Za-z_$][\w$]*)\s*[=<;]`)
	reTSVar       = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?(const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::\s*[^=;]+)?=\s*(.*)$`)
	reTSArrowRHS  = regexp.MustCompile(`^(?:async\s+)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::\s*[^=]*?)?\s*=>`)
	reTSMethod    = regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|async|abstract|override|readonly)\s+)*(?:get\s+|set\s+)?\*?\s*([A-Za-z_$][\w$]*)\s*(?:<[^(:;]*>)?\s*\(`)
)

// tsMethodNameDenylist are identifiers that look like method declarations to
// the heuristic but are control-flow keywords.
var tsMethodNameDenylist = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true,
	"return": true, "do": true, "else": true, "new": true, "typeof": true,
	"function": true, "case": true, "with": true,
}

// ctxKind classifies an open block on the parser's context stack.
type ctxKind int

const (
	ctxNamespace ctxKind = iota // symbols allowed, no methods
	ctxClass                    // methods allowed
	ctxInterface                // methods (member signatures) allowed
	ctxFunction                 // suppress all symbol detection
)

type ctxEntry struct {
	kind  ctxKind
	depth int // brace depth before the context's opening line
}

// parseTypeScript extracts top-level symbols from TS/JS source. baseLine is
// added to every reported line number so embedded scripts (Svelte/Astro) can
// report positions in the host file; pass 0 for standalone files.
func parseTypeScript(file string, src []byte, baseLine int) []Symbol {
	code := cleanTSLines(src)
	var symbols []Symbol
	var stack []ctxEntry
	braceDepth := 0

	top := func() (ctxKind, bool) {
		if len(stack) == 0 {
			return 0, false
		}
		return stack[len(stack)-1].kind, true
	}

	for i, line := range code {
		trimmed := strings.TrimSpace(line)
		delta := 0
		if trimmed != "" {
			kind, inCtx := top()
			suppressed := inCtx && kind == ctxFunction

			var sym *Symbol
			var push ctxKind = -1
			switch {
			case suppressed:
				// Inside a function body: locals and closures are noise.
			case inCtx && (kind == ctxClass || kind == ctxInterface):
				sym, push = detectTSClassMember(file, line, i, baseLine)
				if sym == nil {
					sym, push = detectTSDeclaration(file, line, i, baseLine)
				}
			default:
				sym, push = detectTSDeclaration(file, line, i, baseLine)
			}

			if sym != nil {
				delta = netBraceDelta(line)
				sym.LineEnd = baseLine + tsDeclEnd(code, i) + 1
				symbols = append(symbols, *sym)
				if delta > 0 && push != -1 {
					stack = append(stack, ctxEntry{kind: push, depth: braceDepth})
				}
				braceDepth += delta
			} else {
				braceDepth += netBraceDelta(line)
			}
		}
		for len(stack) > 0 && braceDepth <= stack[len(stack)-1].depth {
			stack = stack[:len(stack)-1]
		}
	}
	return symbols
}

// detectTSDeclaration matches top-level declarations: function, class,
// interface, enum, namespace, type alias, and const/let/var (including
// arrow-function constants). It returns the symbol and the context kind to
// push when the declaration opens a block, or -1 for no context.
func detectTSDeclaration(file, line string, idx, baseLine int) (*Symbol, ctxKind) {
	if m := reTSFunc.FindStringSubmatch(line); m != nil {
		return newTSSymbol(m[1], KindFunc, file, line, idx, baseLine), ctxFunction
	}
	if m := reTSClass.FindStringSubmatch(line); m != nil {
		return newTSSymbol(m[1], KindType, file, line, idx, baseLine), ctxClass
	}
	if m := reTSInterface.FindStringSubmatch(line); m != nil {
		return newTSSymbol(m[1], KindType, file, line, idx, baseLine), ctxInterface
	}
	if m := reTSEnum.FindStringSubmatch(line); m != nil {
		return newTSSymbol(m[1], KindType, file, line, idx, baseLine), -1
	}
	if m := reTSNamespace.FindStringSubmatch(line); m != nil {
		return newTSSymbol(m[1], KindType, file, line, idx, baseLine), ctxNamespace
	}
	if m := reTSTypeAlias.FindStringSubmatch(line); m != nil {
		return newTSSymbol(m[1], KindType, file, line, idx, baseLine), -1
	}
	if m := reTSVar.FindStringSubmatch(line); m != nil {
		declKind := KindVar
		push := ctxKind(-1)
		rhs := strings.TrimSpace(m[3])
		if reTSArrowRHS.MatchString(rhs) || strings.HasPrefix(rhs, "function") {
			declKind = KindFunc
			push = ctxFunction
		} else if m[1] == "const" {
			declKind = KindConst
		}
		return newTSSymbol(m[2], declKind, file, line, idx, baseLine), push
	}
	return nil, -1
}

// detectTSClassMember matches method declarations inside class bodies and
// member signatures inside interfaces.
func detectTSClassMember(file, line string, idx, baseLine int) (*Symbol, ctxKind) {
	m := reTSMethod.FindStringSubmatch(line)
	if m == nil || tsMethodNameDenylist[m[1]] {
		return nil, -1
	}
	return newTSSymbol(m[1], KindMethod, file, line, idx, baseLine), ctxFunction
}

// newTSSymbol builds a symbol with a one-line signature trimmed from the
// declaration line. LineStart is converted to 1-based and offset by baseLine;
// LineEnd is filled in by the caller after the end-of-block scan.
func newTSSymbol(name string, kind SymbolKind, file, line string, idx, baseLine int) *Symbol {
	return &Symbol{
		Name:      name,
		Kind:      kind,
		FilePath:  file,
		LineStart: baseLine + idx + 1,
		LineEnd:   baseLine + idx + 1,
		Signature: tsSignature(line),
	}
}

// tsSignature renders a compact single-line signature from a declaration
// line: trimmed, cut at the opening brace, and capped in length.
func tsSignature(line string) string {
	sig := strings.TrimSpace(line)
	if i := strings.Index(sig, "{"); i >= 0 {
		sig = strings.TrimSpace(sig[:i])
	}
	sig = strings.Join(strings.Fields(sig), " ")
	const maxSig = 200
	if len(sig) > maxSig {
		sig = sig[:maxSig] + "..."
	}
	return sig
}

// netBraceDelta counts unquoted, uncommented { minus } on an already-cleaned
// line.
func netBraceDelta(line string) int {
	delta := 0
	for _, r := range line {
		switch r {
		case '{':
			delta++
		case '}':
			delta--
		}
	}
	return delta
}

// tsDeclEnd finds the 0-based index of the line where the declaration
// starting at line start ends: the matching close brace for block bodies, or
// the end of a continued statement (multi-line signatures, union types).
func tsDeclEnd(lines []string, start int) int {
	braces, parens, brackets := 0, 0, 0
	// blockOpen becomes true only when a line ENDS with unclosed braces, i.e.
	// the declaration has a real body block; inline literals that open and
	// close on one line (e.g. union members like `| { ok: true }`) don't count.
	blockOpen := false
	const maxScan = 2000
	for j := start; j < len(lines) && j < start+maxScan; j++ {
		l := lines[j]
		for _, r := range l {
			switch r {
			case '{':
				braces++
			case '}':
				braces--
			case '(':
				parens++
			case ')':
				parens--
			case '[':
				brackets++
			case ']':
				brackets--
			}
		}
		t := strings.TrimSpace(l)
		switch {
		case braces > 0:
			blockOpen = true
			continue
		case parens > 0 || brackets > 0:
			continue
		case blockOpen:
			return j
		case t == "":
			continue
		case tsLineContinues(t):
			continue
		case j+1 < len(lines) && tsNextLineContinues(lines[j+1]):
			// Union types and chained expressions put the operator first.
			continue
		default:
			return j
		}
	}
	end := start + maxScan - 1
	if end >= len(lines) {
		end = len(lines) - 1
	}
	return end
}

// tsLineContinues reports whether a trimmed line clearly continues onto the
// next line (open statement: operators, separators, or JSX/union fragments).
func tsLineContinues(t string) bool {
	for _, suffix := range []string{"=", "|", "&", ":", ",", ".", "?", "=>", "+", "-", "*", "/", "<", ">", ";"} {
		if strings.HasSuffix(t, suffix) {
			// A trailing ";" normally ends a statement, except inside the rare
			// brace-free constructs we still want to extend; keep it terminal.
			return suffix != ";"
		}
	}
	return false
}

// tsNextLineContinues reports whether the next line starts with a leading
// operator (union member, intersection, method chain), meaning the current
// declaration continues.
func tsNextLineContinues(next string) bool {
	t := strings.TrimSpace(next)
	for _, prefix := range []string{"|", "&", "."} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// --- line cleaning ---------------------------------------------------------

// Lexer states for cleanTSLines.
const (
	tsStCode = iota
	tsStLineComment
	tsStBlockComment
	tsStSingle
	tsStDouble
	tsStTemplate
	tsStRegex
)

// cleanTSLines strips comments and blanks string/template/regex contents so
// that symbol regexes and brace counting are not confused by their contents.
// Template literal ${...} expressions are re-entered as code so braces inside
// them stay balanced. Line count and ordering are preserved.
func cleanTSLines(src []byte) []string {
	raw := string(src)
	lines := make([]string, 0, strings.Count(raw, "\n")+1)
	var b strings.Builder

	state := tsStCode
	// templateStack holds the brace depth at each ${ so the matching } drops
	// back to template state.
	var templateStack []int
	braceDepth := 0
	prevSignificant := byte(0) // last non-space code byte, for regex vs division

	flushLine := func() {
		lines = append(lines, b.String())
		b.Reset()
	}

	i := 0
	for i < len(raw) {
		c := raw[i]
		if c == '\n' {
			if state == tsStLineComment {
				state = tsStCode
			}
			// A line continuation keeps single/double strings open across the
			// newline; template literals span lines naturally.
			if (state == tsStSingle || state == tsStDouble) && b.Len() > 0 {
				s := b.String()
				if strings.HasSuffix(s, "\\") {
					// Stay in string state.
				} else {
					state = tsStCode
				}
			}
			flushLine()
			i++
			continue
		}

		switch state {
		case tsStLineComment:
			// Drop comment bytes.
		case tsStBlockComment:
			if c == '*' && i+1 < len(raw) && raw[i+1] == '/' {
				state = tsStCode
				i++
			}
		case tsStSingle, tsStDouble:
			quote := byte('\'')
			if state == tsStDouble {
				quote = '"'
			}
			if c == '\\' && i+1 < len(raw) {
				i++ // skip escaped byte
			} else if c == quote {
				state = tsStCode
				prevSignificant = quote
			}
		case tsStTemplate:
			if c == '\\' && i+1 < len(raw) {
				i++
			} else if c == '`' {
				state = tsStCode
				prevSignificant = '`'
			} else if c == '$' && i+1 < len(raw) && raw[i+1] == '{' {
				templateStack = append(templateStack, braceDepth)
				braceDepth++
				b.WriteString("${")
				state = tsStCode
				prevSignificant = '{'
				i++
			}
		case tsStRegex:
			if c == '\\' && i+1 < len(raw) {
				i++
			} else if c == '[' {
				// Character class: skip to its end so a / inside doesn't terminate.
				for i+1 < len(raw) && raw[i+1] != ']' && raw[i+1] != '\n' {
					i++
					if raw[i] == '\\' {
						i++
					}
				}
			} else if c == '/' {
				state = tsStCode
				prevSignificant = '/'
			}
		default: // tsStCode
			switch {
			case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
				state = tsStLineComment
				i++
			case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
				state = tsStBlockComment
				i++
			case c == '\'':
				state = tsStSingle
				b.WriteByte(c)
				prevSignificant = c
			case c == '"':
				state = tsStDouble
				b.WriteByte(c)
				prevSignificant = c
			case c == '`':
				state = tsStTemplate
				b.WriteByte(c)
				prevSignificant = c
			case c == '/' && tsStartsRegex(prevSignificant):
				state = tsStRegex
				b.WriteByte(' ') // blank the regex body
			case c == '{':
				braceDepth++
				b.WriteByte(c)
				prevSignificant = c
			case c == '}':
				if len(templateStack) > 0 && braceDepth == templateStack[len(templateStack)-1]+1 {
					// This } closes a ${...}: return to template state. Write it
					// so the cleaned output keeps the ${...} pair balanced for
					// downstream brace counting.
					templateStack = templateStack[:len(templateStack)-1]
					braceDepth--
					b.WriteByte('}')
					state = tsStTemplate
					i++
					continue
				}
				braceDepth--
				b.WriteByte(c)
				prevSignificant = c
			default:
				b.WriteByte(c)
				if c != ' ' && c != '\t' && c != '\r' {
					prevSignificant = c
				}
			}
		}
		i++
	}
	flushLine()
	return lines
}

// tsStartsRegex guesses whether a '/' begins a regex literal rather than a
// division: after an operand (identifier char, digit, ), ], }, quote) it is
// division; after operators, punctuation, or at line start it is a regex.
func tsStartsRegex(prev byte) bool {
	if prev == 0 {
		return true
	}
	if prev >= 'a' && prev <= 'z' || prev >= 'A' && prev <= 'Z' || prev >= '0' && prev <= '9' {
		return false
	}
	switch prev {
	case '_', '$', ')', ']', '}', '\'', '"', '`':
		return false
	}
	return true
}
