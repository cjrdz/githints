package lang

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoParserExtractsSymbolsAndImports(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "sample.go"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := GoParser{}
	if p.Language() != "go" {
		t.Errorf("Language = %q, want go", p.Language())
	}
	if exts := p.Extensions(); len(exts) != 1 || exts[0] != ".go" {
		t.Errorf("Extensions = %v, want [.go]", exts)
	}

	symbols, imports, err := p.Parse("cmd/sample.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(imports) != 2 {
		t.Fatalf("imports = %d, want 2", len(imports))
	}
	if imports[0].ImportedPath != "fmt" || imports[1].ImportedPath != "os" {
		t.Errorf("imports = %v", imports)
	}

	want := map[string]SymbolKind{
		"User":           KindType,
		"MaxRetries":     KindConst,
		"defaultTimeout": KindVar,
		"NewUser":        KindFunc,
		"String":         KindMethod,
	}
	if len(symbols) != len(want) {
		t.Fatalf("symbols = %d, want %d: %v", len(symbols), len(want), symbols)
	}
	for _, sym := range symbols {
		if sym.Kind != want[sym.Name] {
			t.Errorf("%s: kind = %q, want %q", sym.Name, sym.Kind, want[sym.Name])
		}
		if sym.FilePath != "cmd/sample.go" {
			t.Errorf("%s: FilePath = %q", sym.Name, sym.FilePath)
		}
		if sym.LineStart <= 0 {
			t.Errorf("%s: LineStart = %d", sym.Name, sym.LineStart)
		}
		if sym.LineEnd < sym.LineStart {
			t.Errorf("%s: LineEnd < LineStart", sym.Name)
		}
	}
}

func TestGoParserMalformed(t *testing.T) {
	// Missing closing brace. ParseFile returns a partial file with the func.
	// This source must stay inline: a malformed .go file on disk would fail
	// the CI gofmt gate, so it cannot live in testdata/.
	src := []byte(`package broken
func Incomplete() {
	fmt.Println("oops")
`)

	p := GoParser{}
	symbols, imports, err := p.Parse("broken.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) == 0 {
		t.Error("expected at least one symbol from partial parse")
	}
	found := false
	for _, sym := range symbols {
		if sym.Name == "Incomplete" {
			found = true
		}
	}
	if !found {
		t.Errorf("symbols = %v", symbols)
	}
	if len(imports) != 0 {
		t.Errorf("imports = %v, want empty", imports)
	}
}

func TestGoParserHandlesEmptyFile(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "empty.go"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := GoParser{}
	symbols, imports, err := p.Parse("empty.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 0 || len(imports) != 0 {
		t.Errorf("symbols = %v, imports = %v", symbols, imports)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	got := r.Languages()
	if len(got) != 4 {
		t.Errorf("Languages = %v, want 4 languages", got)
	}
	for _, name := range []string{"go", "typescript", "svelte", "astro"} {
		if p := r.ForLanguage(name); p == nil {
			t.Errorf("ForLanguage(%s) = nil", name)
		}
	}
	if p := r.ForPath("foo.go"); p == nil {
		t.Error("ForPath(foo.go) = nil")
	}
	if p := r.ForPath("foo.ts"); p == nil {
		t.Error("ForPath(foo.ts) = nil")
	}
	if p := r.ForPath("foo.svelte"); p == nil {
		t.Error("ForPath(foo.svelte) = nil")
	}
	if p := r.ForPath("foo.astro"); p == nil {
		t.Error("ForPath(foo.astro) = nil")
	}
	if p := r.ForLanguage("rust"); p != nil {
		t.Error("ForLanguage(rust) should be nil")
	}

	parsers, err := r.ResolveLanguages([]string{"go"})
	if err != nil {
		t.Fatalf("ResolveLanguages: %v", err)
	}
	if len(parsers) != 1 {
		t.Fatalf("parsers = %d", len(parsers))
	}
	parsers, err = r.ResolveLanguages([]string{"typescript", "svelte", "astro"})
	if err != nil {
		t.Fatalf("ResolveLanguages: %v", err)
	}
	if len(parsers) != 3 {
		t.Fatalf("parsers = %d", len(parsers))
	}
	if _, err := r.ResolveLanguages([]string{"go", "rust"}); err == nil {
		t.Error("expected unsupported language error")
	}
}

func TestIndexNotePath(t *testing.T) {
	root := "/repo"
	got, collision, err := IndexNotePath(root, "cmd/api/main.go")
	if err != nil {
		t.Fatalf("IndexNotePath: %v", err)
	}
	if collision {
		t.Fatal("expected no collision")
	}
	want := filepath.Join("/repo", ".githints", "index", "cmd", "api", "main.go.md")
	if got != want {
		t.Errorf("IndexNotePath = %q, want %q", got, want)
	}
}

func TestIndexNotePathCollision(t *testing.T) {
	_, collision, err := IndexNotePath("/repo", "index/sneaky.go")
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !collision {
		t.Fatal("expected collision=true")
	}
}

func TestEncodeDecodeLanguageCounts(t *testing.T) {
	m := map[string]int{"go": 5, "typescript": 2}
	s, err := EncodeLanguageCounts(m)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeLanguageCounts(s)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got) != len(m) {
		t.Fatalf("Decode = %v", got)
	}
	for k, v := range m {
		if got[k] != v {
			t.Errorf("%s: got %d, want %d", k, got[k], v)
		}
	}
}

func TestEscapeLike(t *testing.T) {
	if got := EscapeLike("100%"); got != "100\\%" {
		t.Errorf("EscapeLike(100%%) = %q", got)
	}
	if got := EscapeLike("foo_bar"); got != "foo\\_bar" {
		t.Errorf("EscapeLike(foo_bar) = %q", got)
	}
}
