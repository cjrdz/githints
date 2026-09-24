package lang

import (
	"testing"
)

func mustSpecParser(t *testing.T, specJSON string) *SpecParser {
	t.Helper()
	spec, err := LoadSpec([]byte(specJSON))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	p, err := NewSpecParser(spec)
	if err != nil {
		t.Fatalf("NewSpecParser: %v", err)
	}
	return p
}

func symbolNames(symbols []Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, s.Name)
	}
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSpecParserPython(t *testing.T) {
	p := mustSpecParser(t, pythonSpecJSON)

	if p.Language() != "python" {
		t.Errorf("Language = %q", p.Language())
	}
	if exts := p.Extensions(); len(exts) != 1 || exts[0] != ".py" {
		t.Errorf("Extensions = %v", exts)
	}

	src := []byte("" +
		"import os\n" + // 1
		"from pkg.mod import thing\n" + // 2
		"\n" + // 3
		"class Greeter:\n" + // 4
		"    \"\"\"def ghost(): pass\"\"\"\n" + // 5  docstring, must be blanked
		"\n" + // 6
		"    def greet(self, name):\n" + // 7  method: nested in a class, kept
		"        def inner():\n" + // 8  local: inside a function, skipped
		"            pass\n" + // 9
		"        return inner\n" + // 10
		"\n" + // 11
		"def top(a, b):\n" + // 12
		"    return a\n") // 13

	symbols, imports, err := p.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{"Greeter", "greet", "top"}
	if got := symbolNames(symbols); !equalStringSlices(got, want) {
		t.Errorf("symbols = %v, want %v", got, want)
	}

	byName := map[string]Symbol{}
	for _, s := range symbols {
		byName[s.Name] = s
	}
	if got := byName["Greeter"]; got.LineStart != 4 || got.Kind != KindType {
		t.Errorf("Greeter = line %d kind %s, want line 4 kind type", got.LineStart, got.Kind)
	}
	if got := byName["greet"]; got.LineStart != 7 {
		t.Errorf("greet LineStart = %d, want 7", got.LineStart)
	}
	if got := byName["greet"].Signature; got != "self, name" {
		t.Errorf("greet signature = %q", got)
	}
	// The class body runs to the end of the file.
	if got := byName["Greeter"].LineEnd; got != 10 {
		t.Errorf("Greeter LineEnd = %d, want 10", got)
	}
	if got := byName["top"].LineEnd; got != 13 {
		t.Errorf("top LineEnd = %d, want 13", got)
	}

	wantImports := []string{"os", "pkg.mod"}
	got := make([]string, 0, len(imports))
	for _, im := range imports {
		got = append(got, im.ImportedPath)
	}
	if !equalStringSlices(got, wantImports) {
		t.Errorf("imports = %v, want %v", got, wantImports)
	}
}

// TestSpecParserSkipsFunctionLocals is the behaviour that keeps an index
// readable: a closure or helper defined inside a function is not part of the
// file's interface.
func TestSpecParserSkipsFunctionLocals(t *testing.T) {
	p := mustSpecParser(t, pythonSpecJSON)
	src := []byte("def outer():\n    def a():\n        def b():\n            pass\n\ndef after():\n    pass\n")

	symbols, _, err := p.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"outer", "after"}
	if got := symbolNames(symbols); !equalStringSlices(got, want) {
		t.Errorf("symbols = %v, want %v (locals must be skipped, and the stack must unwind)", got, want)
	}
}

const goishSpecJSON = `{
  "language": "goish",
  "extensions": [".goish"],
  "depth_style": "brace",
  "comments": { "line": ["//"], "block": [["/*", "*/"]] },
  "strings": [
    { "open": "\"", "close": "\"", "escape": "\\", "continue_on_escape": true },
    { "open": "` + "`" + `", "close": "` + "`" + `", "multiline": true }
  ],
  "symbols": [
    { "kind": "func", "requires": "func", "pattern": "^func\\s+(?P<name>\\w+)\\s*\\((?P<sig>[^)]*)\\)" },
    { "kind": "method", "requires": "func", "pattern": "^func\\s+\\([^)]*\\)\\s+(?P<name>\\w+)\\s*\\((?P<sig>[^)]*)\\)" },
    { "kind": "type", "requires": "type", "pattern": "^type\\s+(?P<name>\\w+)" }
  ],
  "imports": [
    { "requires": "\"", "pattern": "^\\s*\"(?P<path>[^\"]+)\"" }
  ]
}`

func TestSpecParserBraceDepth(t *testing.T) {
	p := mustSpecParser(t, goishSpecJSON)

	src := []byte("" +
		"type Config struct {\n" + // 1
		"\tName string\n" + // 2
		"}\n" + // 3
		"\n" + // 4
		"func Run(a int) error {\n" + // 5
		"\ttype local struct{}\n" + // 6  inside a function body, skipped
		"\tif a > 0 {\n" + // 7
		"\t\treturn nil\n" + // 8
		"\t}\n" + // 9
		"\treturn nil\n" + // 10
		"}\n" + // 11
		"\n" + // 12
		"func After() {}\n") // 13

	symbols, _, err := p.Parse("a.goish", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"Config", "Run", "After"}
	if got := symbolNames(symbols); !equalStringSlices(got, want) {
		t.Errorf("symbols = %v, want %v", got, want)
	}

	byName := map[string]Symbol{}
	for _, s := range symbols {
		byName[s.Name] = s
	}
	if got := byName["Config"]; got.LineStart != 1 || got.LineEnd != 3 {
		t.Errorf("Config = %d-%d, want 1-3", got.LineStart, got.LineEnd)
	}
	// Run's body contains a nested block; the scan must balance to its own.
	if got := byName["Run"]; got.LineStart != 5 || got.LineEnd != 11 {
		t.Errorf("Run = %d-%d, want 5-11", got.LineStart, got.LineEnd)
	}
	if got := byName["After"]; got.LineStart != 13 || got.LineEnd != 13 {
		t.Errorf("After = %d-%d, want 13-13", got.LineStart, got.LineEnd)
	}
}

// TestSpecParserIgnoresBracesInStringsAndComments is what makes brace depth
// trustworthy at all: depth is counted on blanked lines, never on raw source.
//
// The shape matters. An unbalanced brace inside a string leaves the enclosing
// function looking permanently open, so every later top-level declaration is
// judged to be inside a function body and silently dropped.
func TestSpecParserIgnoresBracesInStringsAndComments(t *testing.T) {
	p := mustSpecParser(t, goishSpecJSON)

	for _, tc := range []struct{ name, src string }{
		{"stringBrace", "func A() {\n\ts := \"{\"\n}\nfunc B() {}\n"},
		{"commentBrace", "func A() {\n\t// {\n}\nfunc B() {}\n"},
		{"blockCommentBrace", "func A() {\n\t/* { { */\n}\nfunc B() {}\n"},
		{"rawStringBrace", "func A() {\n\ts := \x60{\x60\n}\nfunc B() {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			symbols, _, err := p.Parse("a.goish", []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := symbolNames(symbols); !equalStringSlices(got, []string{"A", "B"}) {
				t.Errorf("symbols = %v, want [A B]; an unbalanced brace leaked into depth "+
					"and swallowed the declaration after it", got)
			}
		})
	}
}

func TestSpecParserDepthNoneIndexesEverything(t *testing.T) {
	spec := `{
      "language":"flat","extensions":[".flat"],
      "symbols":[{"kind":"func","requires":"def","pattern":"^\\s*def\\s+(?P<name>\\w+)"}]
    }`
	p := mustSpecParser(t, spec)

	symbols, _, err := p.Parse("a.flat", []byte("def a\n  def b\n    def c\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// With no depth signal nothing can be judged nested, so everything is kept
	// and every declaration is a single line.
	if got := symbolNames(symbols); !equalStringSlices(got, []string{"a", "b", "c"}) {
		t.Errorf("symbols = %v, want [a b c]", got)
	}
	for _, s := range symbols {
		if s.LineEnd != s.LineStart {
			t.Errorf("%s: LineEnd = %d, want %d", s.Name, s.LineEnd, s.LineStart)
		}
	}
}

// TestSpecParserRequiresGateSkipsPattern proves the gate actually gates: a
// rule whose literal is absent must not run its regex.
func TestSpecParserRequiresGateSkipsPattern(t *testing.T) {
	spec := `{
      "language":"gated","extensions":[".g"],
      "symbols":[{"kind":"func","requires":"ZZZ","pattern":"^(?P<name>\\w+)"}]
    }`
	p := mustSpecParser(t, spec)

	symbols, _, err := p.Parse("a.g", []byte("alpha\nbeta\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 0 {
		t.Errorf("gate did not suppress the pattern: %v", symbolNames(symbols))
	}
}

func TestNewSpecParserRejectsInvalidSpec(t *testing.T) {
	if _, err := NewSpecParser(Spec{Language: "x"}); err == nil {
		t.Fatal("expected an invalid spec to be rejected")
	}
}

// TestSpecParserSignatureIsBlanked pins the guarantee documented to users:
// string contents never reach the index, including through a signature.
func TestSpecParserSignatureIsBlanked(t *testing.T) {
	p := mustSpecParser(t, goishSpecJSON)

	symbols, _, err := p.Parse("a.goish", []byte("func A(s string = \"sk-secret-value\") {}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 1 {
		t.Fatalf("symbols = %v", symbolNames(symbols))
	}
	if got := symbols[0].Signature; got != `s string = ""` {
		t.Errorf("signature = %q, want the string contents blanked", got)
	}
}

// TestSpecParserImportsSeeStringContents covers the reason imports are matched
// against a separate view. Most languages put the import path inside a string
// literal, so matching against the fully blanked view would find an empty path
// every time.
func TestSpecParserImportsSeeStringContents(t *testing.T) {
	p := mustSpecParser(t, goishSpecJSON)

	symbols, imports, err := p.Parse("a.goish", []byte("\t\"fmt\"\n\t\"os/exec\"\n\nfunc A() {}\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := make([]string, 0, len(imports))
	for _, im := range imports {
		got = append(got, im.ImportedPath)
	}
	if !equalStringSlices(got, []string{"fmt", "os/exec"}) {
		t.Errorf("imports = %v, want [fmt os/exec]", got)
	}
	if !equalStringSlices(symbolNames(symbols), []string{"A"}) {
		t.Errorf("symbols = %v", symbolNames(symbols))
	}
}

// TestSpecParserIgnoresCommentedImports is the other half: the import view
// keeps strings but must still drop comments, or a commented-out import would
// be recorded as a real dependency and show up in "Imported by".
func TestSpecParserIgnoresCommentedImports(t *testing.T) {
	p := mustSpecParser(t, goishSpecJSON)

	src := []byte("" +
		"\t\"real\"\n" +
		"\t// \"commented\"\n" +
		"\t/* \"blocked\" */\n" +
		"func A() {}\n")

	_, imports, err := p.Parse("a.goish", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := make([]string, 0, len(imports))
	for _, im := range imports {
		got = append(got, im.ImportedPath)
	}
	if !equalStringSlices(got, []string{"real"}) {
		t.Errorf("imports = %v, want only [real]; a commented-out import was indexed", got)
	}
}
