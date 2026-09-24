package lang

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blankerCases are inputs chosen to hit the parts of the lexer that are easy
// to get wrong: regex-versus-division, character classes containing a slash,
// nested interpolation, continuation lines, and delimiters appearing inside
// each other.
var blankerCases = map[string]string{
	"empty":              "",
	"noTrailingNewline":  "let a = 1;",
	"plain":              "const a = \"abc\";\nfunc X() {}\n",
	"lineContinuation":   "const a = \"abc\\\ndef\";\nfunc X() {}\n",
	"multiContinuation":  "const a = \"a\\\nb\\\nc\";\nfunc X() {}\n",
	"singleQuote":        "const a = 'abc';\nlet z = 1;\n",
	"escapedQuote":       "const s = \"a\\\"b\";\nlet z = 1;\n",
	"escapedBackslash":   "const s = \"a\\\\\";\nlet z = 1;\n",
	"regexLiteral":       "const r = /ab+c/;\nconst d = a / b;\n",
	"regexCharClass":     "const r = /[/]/;\nlet z = 1;\n",
	"regexEscaped":       "const r = /a\\/b/;\nlet z = 1;\n",
	"divisionChain":      "let x = y / 2 / 3;\n",
	"regexAfterParen":    "if (/abc/.test(s)) {}\n",
	"template":           "const t = `abc`;\nlet z = 1;\n",
	"templateInterp":     "const t = `a ${x} b`;\nlet z = 1;\n",
	"nestedTemplate":     "const t = `a ${ `b ${c}` } d`;\nlet z = 1;\n",
	"templateBraces":     "const t = `x ${ {a:1} } y`;\nlet z = 1;\n",
	"templateMultiline":  "const t = `line1\nline2`;\nlet z = 1;\n",
	"commentInString":    "const s = \"// not a comment\";\nlet z = 1;\n",
	"blockInString":      "const s = \"/* nope */\";\nlet z = 1;\n",
	"stringInComment":    "// const s = \"x\";\nlet z = 1;\n",
	"stringInBlock":      "/* const s = \"x\"; */\nlet z = 1;\n",
	"blockSpan":          "/* a\nb */ let z = 1;\n",
	"blockUnterminated":  "/* a\nb\n",
	"lineCommentAtEOF":   "let a = 1; // trailing",
	"unterminatedString": "const s = \"abc\nlet z = 1;\n",
	"unterminatedTmpl":   "const t = `abc\nlet z = 1;\n",
	"crlf":               "let a = 1;\r\nlet b = 2;\r\n",
	"bracesOnly":         "function f() { if (x) { return 1; } }\n",
	"emptyString":        "const s = \"\";\nconst u = '';\nconst v = ``;\n",
	"backslashAtEOF":     "const s = \"abc\\",
	"dollarNotInterp":    "const t = `a $ b`;\nlet z = 1;\n",
	"interpNested2":      "const t = `${ `${ `${x}` }` }`;\n",
}

func blankerCorpus(t *testing.T) map[string]string {
	t.Helper()
	corpus := make(map[string]string, len(blankerCases)+4)
	for k, v := range blankerCases {
		corpus[k] = v
	}
	// Real files exercise volume and ordinary shapes the handwritten cases do
	// not. sample.go is not TypeScript, which is the point: the two lexers
	// must agree even on input neither expects.
	for _, name := range []string{"sample.ts", "sample.svelte", "sample.astro", "sample.go"} {
		data, err := os.ReadFile(filepath.Join("..", "testdata", name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		corpus["fixture:"+name] = string(data)
	}
	return corpus
}

// TestBlankerMatchesTypeScriptGolden pins the Blanker against the output of
// cleanTSLines, the hand-written lexer it replaced. The golden file was
// generated from that lexer while both existed, after a differential test and
// 13.1M fuzz executions showed no divergence; it is how that equivalence
// survives the original being deleted.
//
// Regenerate deliberately, never to make a red test green: a diff here means
// the TypeScript parser's view of the source changed.
func TestBlankerMatchesTypeScriptGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "blanker_typescript.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var want map[string][]string
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	bl := NewBlanker(TypeScriptBlankSpec())
	corpus := blankerCorpus(t)

	if len(want) != len(corpus) {
		t.Errorf("golden has %d cases, corpus has %d; regenerate the golden file", len(want), len(corpus))
	}

	for name, src := range corpus {
		t.Run(name, func(t *testing.T) {
			expected, ok := want[name]
			if !ok {
				t.Fatalf("case %q missing from the golden file", name)
			}
			got := bl.Blank([]byte(src))
			if len(got) != len(expected) {
				t.Fatalf("line count = %d, want %d\n got: %q\nwant: %q", len(got), len(expected), got, expected)
			}
			for i := range expected {
				if got[i] != expected[i] {
					t.Errorf("line %d:\n got: %q\nwant: %q", i+1, got[i], expected[i])
				}
			}
		})
	}
}

// TestBlankerPreservesLineCount pins the contract callers actually depend on:
// a symbol found on blanked line N is on source line N.
func TestBlankerPreservesLineCount(t *testing.T) {
	bl := NewBlanker(TypeScriptBlankSpec())

	for name, src := range blankerCorpus(t) {
		want := 1
		for i := 0; i < len(src); i++ {
			if src[i] == '\n' {
				want++
			}
		}
		if got := len(bl.Blank([]byte(src))); got != want {
			t.Errorf("%s: %d lines, want %d", name, got, want)
		}
	}
}

// FuzzBlankerLineCount keeps fuzzing the property that outlived the oracle:
// one output line per input line, on arbitrary bytes. That is the invariant
// every caller relies on to map a matched line back to the source.
func FuzzBlankerLineCount(f *testing.F) {
	for _, src := range blankerCases {
		f.Add([]byte(src))
	}
	bl := NewBlanker(TypeScriptBlankSpec())

	f.Fuzz(func(t *testing.T, src []byte) {
		want := 1
		for i := 0; i < len(src); i++ {
			if src[i] == '\n' {
				want++
			}
		}
		if got := len(bl.Blank(src)); got != want {
			t.Fatalf("%d lines, want %d for %q", got, want, src)
		}
	})
}

// The tests below use specs other than TypeScript. Without them this package
// would only prove the Blanker can do the one job its predecessor did, which
// is not the reason it exists.

func pythonBlankSpec() BlankSpec {
	return BlankSpec{
		LineComments: []string{"#"},
		Strings: []StringRule{
			// Triple quotes must be listed alongside the single ones; the
			// Blanker orders by delimiter length so they win the match.
			{Open: `"""`, Close: `"""`, Escape: '\\', Multiline: true},
			{Open: "'''", Close: "'''", Escape: '\\', Multiline: true},
			{Open: `"`, Close: `"`, Escape: '\\'},
			{Open: "'", Close: "'", Escape: '\\'},
		},
	}
}

func TestBlankerPython(t *testing.T) {
	bl := NewBlanker(pythonBlankSpec())

	src := "" +
		"import os  # trailing comment\n" +
		"\n" +
		"def greet(name):\n" +
		"    \"\"\"Docstring with def notafunction() and a # hash.\"\"\"\n" +
		"    return f'hello {name}'\n"

	got := bl.Blank([]byte(src))
	want := []string{
		"import os  ",
		"",
		"def greet(name):",
		`    """"""`,
		"    return f''",
		"",
	}
	if len(got) != len(want) {
		t.Fatalf("lines = %d, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got: %q\nwant: %q", i+1, got[i], want[i])
		}
	}
}

// TestBlankerPythonDocstringHidesDeclarations is the whole point of blanking:
// a "def" inside a docstring must not look like a definition.
func TestBlankerPythonDocstringHidesDeclarations(t *testing.T) {
	bl := NewBlanker(pythonBlankSpec())

	src := "\"\"\"\ndef ghost():\n    pass\n\"\"\"\ndef real():\n    pass\n"
	got := bl.Blank([]byte(src))

	if len(got) != 7 {
		t.Fatalf("lines = %d, want 7: %q", len(got), got)
	}
	for _, i := range []int{1, 2} {
		if strings.Contains(got[i], "def") {
			t.Errorf("line %d still shows a declaration inside a docstring: %q", i+1, got[i])
		}
	}
	if !strings.Contains(got[4], "def real()") {
		t.Errorf("line 5 should keep the real declaration, got %q", got[4])
	}
}

func sqlBlankSpec() BlankSpec {
	return BlankSpec{
		LineComments:  []string{"--"},
		BlockComments: [][2]string{{"/*", "*/"}},
		Strings: []StringRule{
			{Open: "'", Close: "'", Escape: '\\'},
		},
	}
}

func TestBlankerSQL(t *testing.T) {
	bl := NewBlanker(sqlBlankSpec())

	src := "" +
		"CREATE TABLE users ( -- people\n" +
		"  name TEXT DEFAULT 'anon -- not a comment'\n" +
		"); /* done */\n"

	got := bl.Blank([]byte(src))
	want := []string{
		"CREATE TABLE users ( ",
		"  name TEXT DEFAULT ''",
		"); ",
		"",
	}
	if len(got) != len(want) {
		t.Fatalf("lines = %d, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got: %q\nwant: %q", i+1, got[i], want[i])
		}
	}
}

// TestBlankerNoRegexLiterals pins that division is left alone in languages
// that have no regex literal, where treating '/' as one would eat real code.
func TestBlankerNoRegexLiterals(t *testing.T) {
	bl := NewBlanker(sqlBlankSpec())
	got := bl.Blank([]byte("SELECT a / b / c FROM t;\n"))
	if got[0] != "SELECT a / b / c FROM t;" {
		t.Errorf("division was altered: %q", got[0])
	}
}

// TestBlankerEscapeNeverConsumesNewline pins the three places an escape could
// swallow a line break. All three were found by fuzzing, and each one shifted
// the reported line number of every symbol below it.
//
// The rule is uniform: an escape never consumes a newline. Whether the literal
// survives the break is a separate question, answered by ContinueOnEscape.
func TestBlankerEscapeNeverConsumesNewline(t *testing.T) {
	bl := NewBlanker(TypeScriptBlankSpec())

	for _, tc := range []struct{ name, src string }{
		{"quotedString", "const s = \"a\\\nb\";\n"},
		{"templateLiteral", "const t = `a\\\nb`;\n"},
		{"regexLiteral", "const r = /a\\\nb/;\n"},
		{"regexCharClass", "const r = /[a\\\nb]/;\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := 1
			for i := 0; i < len(tc.src); i++ {
				if tc.src[i] == '\n' {
					want++
				}
			}
			if got := len(bl.Blank([]byte(tc.src))); got != want {
				t.Errorf("%d lines, want %d", got, want)
			}
		})
	}
}

// TestBlankKeepingStrings pins the second view: string contents survive,
// comments do not, and the line count contract still holds.
func TestBlankKeepingStrings(t *testing.T) {
	bl := NewBlanker(TypeScriptBlankSpec())

	src := "" +
		"import x from \"./mod\"; // trailing\n" +
		"const s = 'keep me';\n" +
		"/* gone */ const t = `tpl ${v}`;\n"

	got := bl.BlankKeepingStrings([]byte(src))
	want := []string{
		`import x from "./mod"; `,
		"const s = 'keep me';",
		" const t = `tpl ${v}`;",
		"",
	}
	if len(got) != len(want) {
		t.Fatalf("lines = %d, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got: %q\nwant: %q", i+1, got[i], want[i])
		}
	}
}

// TestBlankKeepingStringsPreservesLineCount checks the contract holds for the
// second view too, including across escaped line breaks.
func TestBlankKeepingStringsPreservesLineCount(t *testing.T) {
	bl := NewBlanker(TypeScriptBlankSpec())
	for name, src := range blankerCorpus(t) {
		want := 1
		for i := 0; i < len(src); i++ {
			if src[i] == '\n' {
				want++
			}
		}
		if got := len(bl.BlankKeepingStrings([]byte(src))); got != want {
			t.Errorf("%s: %d lines, want %d", name, got, want)
		}
	}
}
