package lang

import (
	"strings"
	"testing"
)

// pythonSpecJSON doubles as the worked example a contributor would copy, and
// as proof that JSONC comments survive the loader.
const pythonSpecJSON = `{
  // Python: indentation marks nesting, and docstrings are ordinary strings
  // that happen to hide declarations, so they must be blanked.
  "language": "python",
  "extensions": [".py"],
  "depth_style": "indent",
  "comments": { "line": ["#"] },
  "strings": [
    { "open": "\"\"\"", "close": "\"\"\"", "escape": "\\", "multiline": true },
    { "open": "'''", "close": "'''", "escape": "\\", "multiline": true },
    { "open": "\"", "close": "\"", "escape": "\\" },
    { "open": "'", "close": "'", "escape": "\\" }
  ],
  "symbols": [
    { "kind": "func", "requires": "def", "pattern": "^\\s*(?:async\\s+)?def\\s+(?P<name>\\w+)\\s*\\((?P<sig>[^)]*)\\)" },
    { "kind": "type", "requires": "class", "pattern": "^\\s*class\\s+(?P<name>\\w+)" }
  ],
  "imports": [
    { "requires": "import", "pattern": "^\\s*from\\s+(?P<path>[\\w.]+)\\s+import" },
    { "requires": "import", "pattern": "^\\s*import\\s+(?P<path>[\\w.]+)" }
  ]
}`

func TestLoadSpecPython(t *testing.T) {
	spec, err := LoadSpec([]byte(pythonSpecJSON))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Language != "python" {
		t.Errorf("language = %q", spec.Language)
	}
	if spec.DepthStyle != DepthIndent {
		t.Errorf("depth_style = %q, want indent", spec.DepthStyle)
	}
	if len(spec.Symbols) != 2 || len(spec.Imports) != 2 {
		t.Errorf("symbols = %d, imports = %d", len(spec.Symbols), len(spec.Imports))
	}
	if spec.RegexLiterals {
		t.Error("python has no regex literals")
	}
}

// TestSpecBlankSpecRoundTrip checks the conversion the scan path depends on:
// a spec's string rules must produce a Blanker that actually hides code.
func TestSpecBlankSpecRoundTrip(t *testing.T) {
	spec, err := LoadSpec([]byte(pythonSpecJSON))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}

	bl := NewBlanker(spec.BlankSpec())
	got := bl.Blank([]byte("\"\"\"\ndef ghost():\n    pass\n\"\"\"\ndef real():\n    pass  # tail\n"))

	if len(got) != 7 {
		t.Fatalf("lines = %d, want 7: %q", len(got), got)
	}
	if strings.Contains(got[1], "def") {
		t.Errorf("declaration inside a docstring was not blanked: %q", got[1])
	}
	if !strings.Contains(got[4], "def real()") {
		t.Errorf("real declaration was lost: %q", got[4])
	}
	if strings.Contains(got[5], "tail") {
		t.Errorf("comment was not stripped: %q", got[5])
	}
}

func TestSpecEscapeConvertsToByte(t *testing.T) {
	spec, err := LoadSpec([]byte(pythonSpecJSON))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	for i, rule := range spec.BlankSpec().Strings {
		if rule.Escape != '\\' {
			t.Errorf("strings[%d] escape = %q, want backslash", i, rule.Escape)
		}
	}
}

// TestLoadSpecRejects pins every validation rule. Each case is a mistake a
// contributor could plausibly make, and each would otherwise surface as a
// silently wrong index rather than an error.
func TestLoadSpecRejects(t *testing.T) {
	base := func(body string) string {
		return `{"language":"x","extensions":[".x"],` + body + `}`
	}

	for _, tc := range []struct{ name, json, want string }{
		{"noLanguage", `{"extensions":[".x"],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`, "no language name"},
		{"languageWithColon", `{"language":"c:sharp","extensions":[".x"],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`, "must not contain"},
		{"languageWithComma", `{"language":"a,b","extensions":[".x"],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`, "must not contain"},
		{"badDepthStyle", base(`"depth_style":"tabs","symbols":[{"kind":"func","pattern":"(?P<name>a)"}]`), "unknown depth_style"},
		{"noExtensions", `{"language":"x","symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`, "claims no extensions"},
		{"extensionNoDot", `{"language":"x","extensions":["x"],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`, "must start with"},
		{"duplicateExtension", `{"language":"x","extensions":[".x",".x"],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`, "listed twice"},
		{"noRules", base(`"comments":{"line":["#"]}`), "no symbol or import rules"},
		{"badKind", base(`"symbols":[{"kind":"widget","pattern":"(?P<name>a)"}]`), "unknown kind"},
		{"symbolMissingNameGroup", base(`"symbols":[{"kind":"func","pattern":"def \\w+"}]`), "must capture"},
		{"importMissingPathGroup", base(`"imports":[{"pattern":"import \\w+"}]`), "must capture"},
		{"importWithKind", base(`"imports":[{"kind":"func","pattern":"(?P<path>a)"}]`), "must not set a kind"},
		{"uncompilablePattern", base(`"symbols":[{"kind":"func","pattern":"(?P<name>["}]`), "does not compile"},
		{"emptyPattern", base(`"symbols":[{"kind":"func","pattern":""}]`), "has no pattern"},
		{"multiByteEscape", base(`"strings":[{"open":"'","close":"'","escape":"\\\\x"}],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]`), "single byte"},
		{"halfInterp", base(`"strings":[{"open":"'","close":"'","interp_open":"${"}],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]`), "or neither"},
		{"badInterpClose", base(`"strings":[{"open":"'","close":"'","interp_open":"${","interp_close":")"}],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]`), "must be"},
		{"stringMissingClose", base(`"strings":[{"open":"'"}],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]`), "open and close"},
		{"emptyLineComment", base(`"comments":{"line":[""]},"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]`), "blank every line"},
		{"malformedJSON", `{"language":`, "parse spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadSpec([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadSpecEnforcesLimits(t *testing.T) {
	long := strings.Repeat("a", MaxSpecPatternLen+1)
	_, err := LoadSpec([]byte(`{"language":"x","extensions":[".x"],"symbols":[{"kind":"func","pattern":"(?P<name>` + long + `)"}]}`))
	if err == nil || !strings.Contains(err.Error(), "over the limit") {
		t.Errorf("long pattern should be rejected, got %v", err)
	}

	var exts []string
	for i := 0; i <= MaxSpecExtensions; i++ {
		exts = append(exts, `".x`+string(rune('a'+i%26))+string(rune('a'+i/26))+`"`)
	}
	_, err = LoadSpec([]byte(`{"language":"x","extensions":[` + strings.Join(exts, ",") + `],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`))
	if err == nil || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Errorf("too many extensions should be rejected, got %v", err)
	}
}

func TestLoadSpecNormalizes(t *testing.T) {
	spec, err := LoadSpec([]byte(`{"language":"  PyThon  ","extensions":["  .PY  "],"symbols":[{"kind":"func","pattern":"(?P<name>a)"}]}`))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Language != "python" {
		t.Errorf("language = %q, want python", spec.Language)
	}
	if spec.Extensions[0] != ".py" {
		t.Errorf("extension = %q, want .py", spec.Extensions[0])
	}
	if spec.DepthStyle != DepthNone {
		t.Errorf("depth_style = %q, want the none default", spec.DepthStyle)
	}
}
