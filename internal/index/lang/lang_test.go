package lang

import (
	"os"
	"path/filepath"
	"strings"
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

// unregisteredLanguage is a name no parser will ever claim. Using a plausible
// language here (this test used to use "rust") turns the negative assertions
// into a tripwire that fires the day that language is added.
const unregisteredLanguage = "definitely-not-a-language"

// TestRegistry asserts the invariants that must hold for *any* registry,
// whatever is registered in it. Nothing here counts parsers or names them, so
// adding a language cannot break it; see TestRegistryRegistersKnownLanguages
// for the coverage that a specific parser is still wired up.
func TestRegistry(t *testing.T) {
	r := NewRegistry()

	names := r.Languages()
	if len(names) == 0 {
		t.Fatal("Languages() is empty; no parsers registered")
	}
	if len(names) != len(r.AllParsers()) {
		t.Errorf("Languages() = %d names but AllParsers() = %d parsers", len(names), len(r.AllParsers()))
	}

	seenExt := make(map[string]string)
	for _, name := range names {
		p := r.ForLanguage(name)
		if p == nil {
			t.Errorf("ForLanguage(%q) = nil for a name Languages() reported", name)
			continue
		}
		if got := strings.ToLower(p.Language()); got != name {
			t.Errorf("Languages() reported %q but parser calls itself %q", name, got)
		}

		// Language names are serialized into the meta row as "name:count"
		// pairs joined by commas (EncodeLanguageCounts). ':' is rejected
		// there, but ',' is not and would silently corrupt the decode.
		if strings.ContainsAny(name, ":,") {
			t.Errorf("language %q contains ':' or ',', which EncodeLanguageCounts cannot round-trip", name)
		}

		// Case-insensitive lookup is relied on by config, where users type
		// the language name by hand.
		if r.ForLanguage(strings.ToUpper(name)) != p {
			t.Errorf("ForLanguage(%q) is case-sensitive", name)
		}

		exts := p.Extensions()
		if len(exts) == 0 {
			t.Errorf("%s claims no extensions, so no file can ever select it", name)
		}
		for _, ext := range exts {
			if !strings.HasPrefix(ext, ".") || ext != strings.ToLower(ext) {
				t.Errorf("%s: extension %q must be lower-case and dot-prefixed", name, ext)
			}
			if owner, dup := seenExt[ext]; dup {
				t.Errorf("extension %q claimed by both %s and %s", ext, owner, name)
			}
			seenExt[ext] = name
			if got := r.ForPath("foo" + ext); got != p {
				t.Errorf("ForPath(foo%s) did not resolve back to %s", ext, name)
			}
		}
	}

	if p := r.ForLanguage(unregisteredLanguage); p != nil {
		t.Errorf("ForLanguage(%q) = %v, want nil", unregisteredLanguage, p)
	}

	// ResolveLanguages must accept everything the registry advertises, and
	// preserve the caller's order.
	parsers, err := r.ResolveLanguages(names)
	if err != nil {
		t.Fatalf("ResolveLanguages(%v): %v", names, err)
	}
	if len(parsers) != len(names) {
		t.Fatalf("ResolveLanguages returned %d parsers for %d names", len(parsers), len(names))
	}
	for i, name := range names {
		if strings.ToLower(parsers[i].Language()) != name {
			t.Errorf("ResolveLanguages[%d] = %s, want %s (order not preserved)", i, parsers[i].Language(), name)
		}
	}

	if _, err := r.ResolveLanguages([]string{names[0], unregisteredLanguage}); err == nil {
		t.Error("expected unsupported language error")
	}
}

// TestLanguagesIsSorted pins the ordering. Languages() is built by ranging a
// map, so without an explicit sort the CLI listing and the ResolveLanguages
// error message would both shuffle between runs.
func TestLanguagesIsSorted(t *testing.T) {
	got := NewRegistry().Languages()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("Languages() not sorted: %v", got)
		}
	}
}

// TestResolveLanguagesErrorListsSupported checks the error is actionable. The
// user who sees it has, by definition, guessed wrong about what is supported.
func TestResolveLanguagesErrorListsSupported(t *testing.T) {
	r := NewRegistry()
	_, err := r.ResolveLanguages([]string{unregisteredLanguage})
	if err == nil {
		t.Fatal("expected an error for an unsupported language")
	}
	if !strings.Contains(err.Error(), unregisteredLanguage) {
		t.Errorf("error should name the rejected language, got: %v", err)
	}
	for _, name := range r.Languages() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error should list supported language %q, got: %v", name, err)
		}
	}
}

// TestRegistryRegistersKnownLanguages pins that the parsers shipped today are
// still wired into the registry. It is a subset check on purpose: adding a
// language must not require editing it.
func TestRegistryRegistersKnownLanguages(t *testing.T) {
	r := NewRegistry()
	for _, tc := range []struct{ language, path string }{
		{"go", "foo.go"},
		{"typescript", "foo.ts"},
		{"svelte", "foo.svelte"},
		{"astro", "foo.astro"},
	} {
		p := r.ForLanguage(tc.language)
		if p == nil {
			t.Errorf("ForLanguage(%s) = nil", tc.language)
			continue
		}
		if got := r.ForPath(tc.path); got != p {
			t.Errorf("ForPath(%s) did not resolve to the %s parser", tc.path, tc.language)
		}
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

// TestNoteLinkDefaultResolvesToNoteFile pins the default-mode contract: the
// link target is relative to the linking document and points at the actual
// note file on disk (with .md suffix).
func TestNoteLinkDefaultResolvesToNoteFile(t *testing.T) {
	cases := []struct{ fromDir, display, target, want string }{
		{"index/pkg/lib", "cmd/main.go", "cmd/main.go", "[cmd/main.go](../../cmd/main.go.md)"},
		{"index/src", "src/main.ts", "src/main.ts", "[src/main.ts](main.ts.md)"},
		{"index", "a.go", "a.go", "[a.go](a.go.md)"},
		{".", "example.com/m/pkg/lib", "pkg/lib/lib.go", "[example.com/m/pkg/lib](index/pkg/lib/lib.go.md)"},
	}
	for _, tc := range cases {
		if got := NoteLink(tc.fromDir, tc.display, tc.target, false); got != tc.want {
			t.Errorf("NoteLink(%q, %q, %q) = %q, want %q", tc.fromDir, tc.display, tc.target, got, tc.want)
		}
	}
}

func TestNoteLinkDefaultEncodesTarget(t *testing.T) {
	got := NoteLink("index", "file[weird].go", "file[weird].go", false)
	want := "[file[weird].go](file%5Bweird%5D.go.md)"
	if got != want {
		t.Errorf("NoteLink = %q, want %q", got, want)
	}
}

func TestNoteLinkObsidianIgnoresFromDir(t *testing.T) {
	got := NoteLink("index/pkg", "label", "dir/b.go", true)
	if want := "[[dir/b.go.md|label]]"; got != want {
		t.Errorf("NoteLink = %q, want %q", got, want)
	}
}
