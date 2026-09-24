package lang

import "strings"

// Blanking is the pass every language needs before line-oriented symbol
// matching is safe: comments are removed and string bodies emptied, so a
// regex cannot match a declaration that only appears inside a comment or a
// string literal, and brace counting is not thrown off by a brace in prose.
//
// The contract, which callers depend on:
//
//   - One output line per input line, in order. Line numbers reported against
//     the blanked text are valid against the source.
//   - String and comment *contents* are dropped, not replaced with spaces, so
//     columns are not preserved. Only line numbers are.
//   - String delimiters are kept, so "abc" becomes "" rather than a dangling
//     quote. Downstream matching can still tell a string was there.

// StringRule describes one kind of string literal.
type StringRule struct {
	Open, Close string

	// Escape is the byte that escapes the next byte inside this string; 0 for
	// literals with no escaping, such as raw strings.
	Escape byte

	// Multiline marks a literal that spans newlines with no continuation
	// marker: JS template literals, Python triple-quoted strings.
	Multiline bool

	// ContinueOnEscape marks a literal that spans a newline only when the
	// escape byte immediately precedes it, as in a JS "line one \" split.
	ContinueOnEscape bool

	// InterpOpen and InterpClose bound an interpolation that re-enters code,
	// such as "${" and "}". Both empty means no interpolation. Interpolation
	// is matched by counting { and }, so InterpClose must be "}"; every
	// language with interpolation spells it that way.
	InterpOpen, InterpClose string
}

// BlankSpec is the lexical surface of a language: what hides code from the
// matcher. It is deliberately not a grammar.
type BlankSpec struct {
	LineComments  []string
	BlockComments [][2]string
	Strings       []StringRule

	// RegexLiterals enables JS-style /.../ literals, disambiguated from
	// division by what precedes the slash.
	RegexLiterals bool
}

// Blanker is a compiled BlankSpec.
type Blanker struct {
	lineComments  []string
	blockComments [][2]string
	strings       []StringRule
	regexLiterals bool
}

// NewBlanker compiles a spec. Delimiters are ordered longest-first so that a
// language declaring both `"""` and `"` matches the triple quote first.
func NewBlanker(spec BlankSpec) *Blanker {
	bl := &Blanker{
		lineComments:  append([]string(nil), spec.LineComments...),
		blockComments: append([][2]string(nil), spec.BlockComments...),
		strings:       append([]StringRule(nil), spec.Strings...),
		regexLiterals: spec.RegexLiterals,
	}
	sortByLenDesc(bl.lineComments, func(s string) int { return len(s) })
	sortByLenDesc(bl.blockComments, func(p [2]string) int { return len(p[0]) })
	sortByLenDesc(bl.strings, func(r StringRule) int { return len(r.Open) })
	return bl
}

// sortByLenDesc is an insertion sort on a tiny slice; the lang package
// deliberately avoids importing sort.
func sortByLenDesc[T any](a []T, length func(T) int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && length(a[j]) > length(a[j-1]); j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

type blankState int

const (
	bsCode blankState = iota
	bsLineComment
	bsBlockComment
	bsString
	bsRegex
)

// interpFrame remembers where an interpolation re-entered code, so its closing
// brace can hand control back to the string rule that opened it.
type interpFrame struct {
	braceDepth int
	rule       int
}

// Blank returns one cleaned line per source line.
func (bl *Blanker) Blank(src []byte) []string {
	raw := string(src)
	lines := make([]string, 0, strings.Count(raw, "\n")+1)
	var b strings.Builder

	state := bsCode
	active := 0 // index into strings or blockComments, per state
	var interp []interpFrame
	braceDepth := 0
	prevSignificant := byte(0) // last non-space code byte, for regex vs division
	pendingContinuation := false

	flushLine := func() {
		lines = append(lines, b.String())
		b.Reset()
	}

	i := 0
	for i < len(raw) {
		c := raw[i]

		if c == '\n' {
			if state == bsLineComment {
				state = bsCode
			}
			if state == bsString && !bl.strings[active].Multiline {
				if pendingContinuation {
					pendingContinuation = false
				} else {
					// Unterminated: recover so the damage stops at this line.
					state = bsCode
				}
			}
			flushLine()
			i++
			continue
		}

		switch state {
		case bsLineComment:
			// Dropped.

		case bsBlockComment:
			if end := bl.blockComments[active][1]; strings.HasPrefix(raw[i:], end) {
				state = bsCode
				i += len(end) - 1
			}

		case bsString:
			rule := bl.strings[active]
			switch {
			case rule.Escape != 0 && c == rule.Escape && i+1 < len(raw):
				// An escape never consumes a newline. Hand it to the top of
				// the loop so one source line stays one output line; eating it
				// here would merge them and shift every line number below.
				// ContinueOnEscape only decides whether the literal survives
				// the break, not whether the newline is consumed.
				if raw[i+1] == '\n' {
					pendingContinuation = rule.ContinueOnEscape
				} else {
					i++
				}
			case strings.HasPrefix(raw[i:], rule.Close):
				b.WriteString(rule.Close)
				prevSignificant = rule.Close[len(rule.Close)-1]
				state = bsCode
				i += len(rule.Close) - 1
			case rule.InterpOpen != "" && strings.HasPrefix(raw[i:], rule.InterpOpen):
				interp = append(interp, interpFrame{braceDepth: braceDepth, rule: active})
				braceDepth++
				b.WriteString(rule.InterpOpen)
				prevSignificant = rule.InterpOpen[len(rule.InterpOpen)-1]
				state = bsCode
				i += len(rule.InterpOpen) - 1
			}

		case bsRegex:
			// An escape must never consume a newline: the line count is the
			// one thing every caller relies on. A regex literal cannot legally
			// span a line anyway, so a trailing escape is malformed input.
			if c == '\\' && i+1 < len(raw) && raw[i+1] != '\n' {
				i++
			} else if c == '[' {
				// Character class: skip to its end so a / inside does not
				// terminate the literal.
				for i+1 < len(raw) && raw[i+1] != ']' && raw[i+1] != '\n' {
					i++
					if raw[i] == '\\' && i+1 < len(raw) && raw[i+1] != '\n' {
						i++
					}
				}
			} else if c == '/' {
				state = bsCode
				prevSignificant = '/'
			}

		default: // bsCode
			if n := bl.matchLineComment(raw, i); n > 0 {
				state = bsLineComment
				i += n - 1
				break
			}
			if idx, n := bl.matchBlockComment(raw, i); n > 0 {
				state = bsBlockComment
				active = idx
				i += n - 1
				break
			}
			if idx, n := bl.matchStringOpen(raw, i); n > 0 {
				state = bsString
				active = idx
				b.WriteString(bl.strings[idx].Open)
				prevSignificant = bl.strings[idx].Open[n-1]
				i += n - 1
				break
			}
			switch {
			case bl.regexLiterals && c == '/' && startsRegex(prevSignificant):
				state = bsRegex
				b.WriteByte(' ') // blank the regex body
			case c == '{':
				braceDepth++
				b.WriteByte(c)
				prevSignificant = c
			case c == '}':
				if len(interp) > 0 && braceDepth == interp[len(interp)-1].braceDepth+1 {
					// Closes an interpolation: hand control back to its string.
					frame := interp[len(interp)-1]
					interp = interp[:len(interp)-1]
					braceDepth--
					b.WriteByte('}')
					state = bsString
					active = frame.rule
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

func (bl *Blanker) matchLineComment(raw string, i int) int {
	for _, m := range bl.lineComments {
		if strings.HasPrefix(raw[i:], m) {
			return len(m)
		}
	}
	return 0
}

func (bl *Blanker) matchBlockComment(raw string, i int) (int, int) {
	for idx, pair := range bl.blockComments {
		if strings.HasPrefix(raw[i:], pair[0]) {
			return idx, len(pair[0])
		}
	}
	return 0, 0
}

func (bl *Blanker) matchStringOpen(raw string, i int) (int, int) {
	for idx, rule := range bl.strings {
		if strings.HasPrefix(raw[i:], rule.Open) {
			return idx, len(rule.Open)
		}
	}
	return 0, 0
}

// startsRegex guesses whether a '/' begins a regex literal rather than a
// division: after an operand (identifier char, digit, ), ], }, quote) it is
// division; after operators, punctuation, or at line start it is a regex.
func startsRegex(prev byte) bool {
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

// tsBlanker is the compiled TypeScript spec, shared by every parse. A Blanker
// is immutable once built and Blank keeps all its state in locals, so this is
// safe to use from concurrent scans.
var tsBlanker = NewBlanker(TypeScriptBlankSpec())

// TypeScriptBlankSpec is the lexical surface of the TypeScript/JavaScript
// family. It is also the reference spec: TestBlankerMatchesCleanTSLines pins
// it byte-for-byte against the hand-written lexer it replaces.
func TypeScriptBlankSpec() BlankSpec {
	return BlankSpec{
		LineComments:  []string{"//"},
		BlockComments: [][2]string{{"/*", "*/"}},
		Strings: []StringRule{
			{Open: "'", Close: "'", Escape: '\\', ContinueOnEscape: true},
			{Open: `"`, Close: `"`, Escape: '\\', ContinueOnEscape: true},
			{Open: "`", Close: "`", Escape: '\\', Multiline: true, InterpOpen: "${", InterpClose: "}"},
		},
		RegexLiterals: true,
	}
}
