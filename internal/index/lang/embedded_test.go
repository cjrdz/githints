package lang

import (
	"strings"
	"testing"
)

// TestEmbeddedSpecsAreValid is the build-time guarantee behind loading specs
// without panicking: a spec compiled into the binary must always parse and
// compile. If this fails, a language would silently vanish for users.
func TestEmbeddedSpecsAreValid(t *testing.T) {
	parsers, errs := EmbeddedParsers()
	for _, err := range errs {
		t.Errorf("embedded spec failed to load: %v", err)
	}
	if len(parsers) == 0 {
		t.Fatal("no embedded specs loaded; the go:embed pattern may have stopped matching")
	}
}

// TestEmbeddedSpecsAreRegistered proves the whole point of the mechanism: a
// language present only as a JSON file is usable, with no Go change and no
// edit to NewRegistry.
func TestEmbeddedSpecsAreRegistered(t *testing.T) {
	r := NewRegistry()
	parsers, _ := EmbeddedParsers()

	for _, p := range parsers {
		got := r.ForLanguage(p.Language())
		if got == nil {
			t.Errorf("embedded language %s is not in the registry", p.Language())
			continue
		}
		for _, ext := range p.Extensions() {
			if r.ForPath("x"+ext) != got {
				t.Errorf("%s: extension %s does not resolve to it", p.Language(), ext)
			}
		}
	}
}

func TestPythonIsRegistered(t *testing.T) {
	r := NewRegistry()
	p := r.ForLanguage("python")
	if p == nil {
		t.Fatal("python is not registered")
	}
	if r.ForPath("a.py") != p || r.ForPath("a.pyi") != p {
		t.Error("python does not claim .py and .pyi")
	}
}

// TestSpecCannotDisplaceNativeParser pins the precedence. Native parsers
// register first, so a spec claiming an extension they own is reported and
// skipped rather than quietly replacing a real parser.
func TestSpecCannotDisplaceNativeParser(t *testing.T) {
	r := NewRegistry()

	spec, err := LoadSpec([]byte(`{
		"language":"impostor","extensions":[".go"],
		"symbols":[{"kind":"func","pattern":"^func (?P<name>\\w+)"}]
	}`))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	p, err := NewSpecParser(spec)
	if err != nil {
		t.Fatalf("NewSpecParser: %v", err)
	}

	err = r.registerSpecParser(p)
	if err == nil {
		t.Fatal("a spec was allowed to claim .go")
	}
	if !strings.Contains(err.Error(), "already claimed by go") {
		t.Errorf("error should name the owner, got: %v", err)
	}
	if r.ForPath("x.go").Language() != "go" {
		t.Error("the native Go parser was displaced")
	}
}

func TestSpecCannotDuplicateLanguageName(t *testing.T) {
	r := NewRegistry()

	spec, err := LoadSpec([]byte(`{
		"language":"python","extensions":[".py2"],
		"symbols":[{"kind":"func","pattern":"^def (?P<name>\\w+)"}]
	}`))
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	p, err := NewSpecParser(spec)
	if err != nil {
		t.Fatalf("NewSpecParser: %v", err)
	}
	if err := r.registerSpecParser(p); err == nil {
		t.Fatal("a second parser was allowed to claim the name python")
	}
}

// TestEmbeddedParsersAreCached keeps NewRegistry cheap. It runs on every scan,
// and recompiling every spec's regexes each time would be pure waste.
func TestEmbeddedParsersAreCached(t *testing.T) {
	first, _ := EmbeddedParsers()
	second, _ := EmbeddedParsers()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("parser counts differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("parser %d was rebuilt; specs are not cached", i)
		}
	}
}

// TestPythonSpecParsesRealSource exercises the shipped spec end to end,
// including the cases it is written to get right.
func TestPythonSpecParsesRealSource(t *testing.T) {
	p := NewRegistry().ForLanguage("python")
	if p == nil {
		t.Fatal("python is not registered")
	}

	src := []byte("" +
		"import os\n" + // 1
		"import os.path as osp\n" + // 2
		"from .relative import thing\n" + // 3
		"from pkg.sub import other\n" + // 4
		"\n" + // 5
		"MAX_RETRIES = 3\n" + // 6
		"TIMEOUT: float = 1.5\n" + // 7
		"lowercase_not_a_const = 1\n" + // 8
		"\n" + // 9
		"class Client:\n" + // 10
		"    '''def ghost(): pass'''\n" + // 11
		"\n" + // 12
		"    async def fetch(self, url):\n" + // 13
		"        def retry():\n" + // 14
		"            pass\n" + // 15
		"        return retry\n" + // 16
		"\n" + // 17
		"def helper(a, b=\"# not a comment\"):\n" + // 18
		"    return a\n") // 19

	symbols, imports, err := p.Parse("client.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := map[string]SymbolKind{
		"MAX_RETRIES": KindConst,
		"TIMEOUT":     KindConst,
		"Client":      KindType,
		"fetch":       KindFunc,
		"helper":      KindFunc,
	}
	got := map[string]SymbolKind{}
	for _, s := range symbols {
		got[s.Name] = s.Kind
	}
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: kind %q, want %q", name, got[name], kind)
		}
	}
	for _, unwanted := range []string{"ghost", "retry", "lowercase_not_a_const"} {
		if _, found := got[unwanted]; found {
			t.Errorf("%s should not be indexed", unwanted)
		}
	}
	if len(got) != len(want) {
		t.Errorf("symbols = %v, want exactly %v", got, want)
	}

	wantImports := []string{"os", "os.path", ".relative", "pkg.sub"}
	gotImports := make([]string, 0, len(imports))
	for _, im := range imports {
		gotImports = append(gotImports, im.ImportedPath)
	}
	if !equalStringSlices(gotImports, wantImports) {
		t.Errorf("imports = %v, want %v", gotImports, wantImports)
	}
}
