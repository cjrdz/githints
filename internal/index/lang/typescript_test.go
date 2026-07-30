package lang

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// symbolKey flattens a symbol to a comparable, sortable string.
func symbolKey(s Symbol) string {
	return string(s.Kind) + " " + s.Name
}

func sortedSymbolKeys(symbols []Symbol) []string {
	keys := make([]string, 0, len(symbols))
	for _, s := range symbols {
		keys = append(keys, symbolKey(s))
	}
	sort.Strings(keys)
	return keys
}

func sortedImportPaths(imports []Import) []string {
	paths := make([]string, 0, len(imports))
	for _, im := range imports {
		paths = append(paths, im.ImportedPath)
	}
	sort.Strings(paths)
	return paths
}

func findSymbol(t *testing.T, symbols []Symbol, name string, kind SymbolKind) Symbol {
	t.Helper()
	for _, s := range symbols {
		if s.Name == name && s.Kind == kind {
			return s
		}
	}
	t.Fatalf("symbol %s (%s) not found in %v", name, kind, symbols)
	return Symbol{}
}

func TestTypeScriptParserExtractsSymbolsAndImports(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "sample.ts"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := TypeScriptParser{}
	if p.Language() != "typescript" {
		t.Errorf("Language = %q, want typescript", p.Language())
	}

	symbols, imports, err := p.Parse("src/features/catalog/sample.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{
		"const MAX_RETRIES",
		"const dynamic",
		"const legacy",
		"func buildQuery",
		"func fetchUser",
		"method constructor",
		"method findAll", // interface member
		"method findAll", // class method
		"method query",
		"method size",
		"type BaseService",
		"type Result",
		"type Role",
		"type UserID",
		"type UserService",
		"var mutableCounter",
	}
	sort.Strings(want)
	got := sortedSymbolKeys(symbols)
	if len(got) != len(want) {
		t.Fatalf("symbols = %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("symbols = %v\nwant %v", got, want)
			break
		}
	}

	// Spot-check line numbers and ranges.
	if s := findSymbol(t, symbols, "BaseService", KindType); s.LineStart != 52 || s.LineEnd != 68 {
		t.Errorf("BaseService lines = %d-%d, want 52-68", s.LineStart, s.LineEnd)
	}
	if s := findSymbol(t, symbols, "Result", KindType); s.LineStart != 26 || s.LineEnd != 28 {
		t.Errorf("Result lines = %d-%d, want 26-28 (union type continuation)", s.LineStart, s.LineEnd)
	}
	if s := findSymbol(t, symbols, "fetchUser", KindFunc); s.LineStart != 43 || s.LineEnd != 50 {
		t.Errorf("fetchUser lines = %d-%d, want 43-50", s.LineStart, s.LineEnd)
	}
	for _, s := range symbols {
		if s.FilePath != "src/features/catalog/sample.ts" {
			t.Errorf("%s: FilePath = %q", s.Name, s.FilePath)
		}
		if s.LineEnd < s.LineStart {
			t.Errorf("%s: LineEnd < LineStart", s.Name)
		}
	}

	// Function-body locals and closures must be suppressed.
	for _, s := range symbols {
		if s.Name == "localHelper" || s.Name == "innerClosure" {
			t.Errorf("function-local symbol %s should not be indexed", s.Name)
		}
	}

	wantImports := []string{
		"@core/di",
		"@shared/utils",
		"src/features/catalog/cjs-dep",
		"src/features/catalog/helpers",
		"src/features/catalog/lazy-module",
		"src/features/catalog/logging",
		"src/features/catalog/re-exports",
		"src/features/types/user",
	}
	gotImports := sortedImportPaths(imports)
	if len(gotImports) != len(wantImports) {
		t.Fatalf("imports = %v\nwant %v", gotImports, wantImports)
	}
	for i := range wantImports {
		if gotImports[i] != wantImports[i] {
			t.Errorf("imports = %v\nwant %v", gotImports, wantImports)
			break
		}
	}
}

func TestTypeScriptImportNormalization(t *testing.T) {
	src := []byte(`import a from "./foo";
import b from "./foo.ts";
import c from "./baz/index";
import d from "../shared/util.svelte";
import e from "@core/di";
import f from "svelte";
import g from "./styles.css";
`)
	p := TypeScriptParser{}
	_, imports, err := p.Parse("src/features/catalog/comp.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := sortedImportPaths(imports)
	want := []string{
		"@core/di",
		"src/features/catalog/baz",        // /index stripped
		"src/features/catalog/foo",        // ./foo and ./foo.ts dedupe
		"src/features/catalog/styles.css", // non-code extension kept
		"src/features/shared/util",        // .svelte extension stripped
		"svelte",
	}
	if len(got) != len(want) {
		t.Fatalf("imports = %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("imports = %v\nwant %v", got, want)
			break
		}
	}
}

func TestTypeScriptParserHandlesCommentsAndTemplates(t *testing.T) {
	// Declarations inside comments and strings must not be indexed; template
	// literal ${} expressions must not desync brace matching.
	src := []byte("// function commentedOut() {}\n" +
		"/* class AlsoCommented {} */\n" +
		"const s = \"function inString() {}\";\n" +
		"export function real() {\n" +
		"\tconst tmpl = `value: ${1 + 2} and ${{ nested: true }}`;\n" +
		"\treturn tmpl;\n" +
		"}\n" +
		"export const after = 1;\n")

	p := TypeScriptParser{}
	symbols, _, err := p.Parse("x.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := sortedSymbolKeys(symbols)
	want := []string{"const after", "const s", "func real"}
	if len(got) != len(want) {
		t.Fatalf("symbols = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("symbols = %v, want %v", got, want)
		}
	}
	if s := findSymbol(t, symbols, "real", KindFunc); s.LineEnd != 7 {
		t.Errorf("real LineEnd = %d, want 7 (template braces must balance)", s.LineEnd)
	}
}

func TestTypeScriptParserHandlesEmptyFile(t *testing.T) {
	p := TypeScriptParser{}
	symbols, imports, err := p.Parse("empty.ts", []byte(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(symbols) != 0 || len(imports) != 0 {
		t.Errorf("symbols = %v, imports = %v", symbols, imports)
	}
}

func TestSvelteParserExtractsScriptBlocks(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "sample.svelte"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := SvelteParser{}
	if p.Language() != "svelte" {
		t.Errorf("Language = %q, want svelte", p.Language())
	}

	symbols, imports, err := p.Parse("src/features/catalog/sample.svelte", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{
		"const MODULE_VERSION",
		"const displayName",
		"func load",
		"func refresh",
		"type Props",
		"var user",
	}
	got := sortedSymbolKeys(symbols)
	if len(got) != len(want) {
		t.Fatalf("symbols = %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("symbols = %v\nwant %v", got, want)
			break
		}
	}

	// Line numbers must be in host-file coordinates (module script at top).
	if s := findSymbol(t, symbols, "MODULE_VERSION", KindConst); s.LineStart != 2 {
		t.Errorf("MODULE_VERSION LineStart = %d, want 2", s.LineStart)
	}
	if s := findSymbol(t, symbols, "Props", KindType); s.LineStart != 10 {
		t.Errorf("Props LineStart = %d, want 10", s.LineStart)
	}
	if s := findSymbol(t, symbols, "refresh", KindFunc); s.LineStart != 21 || s.LineEnd != 23 {
		t.Errorf("refresh lines = %d-%d, want 21-23", s.LineStart, s.LineEnd)
	}

	gotImports := sortedImportPaths(imports)
	wantImports := []string{"src/lib/api", "src/types/user", "svelte"}
	if len(gotImports) != len(wantImports) {
		t.Fatalf("imports = %v\nwant %v", gotImports, wantImports)
	}
	for i := range wantImports {
		if gotImports[i] != wantImports[i] {
			t.Errorf("imports = %v\nwant %v", gotImports, wantImports)
			break
		}
	}
}

func TestAstroParserExtractsFrontmatterAndScripts(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "sample.astro"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	p := AstroParser{}
	if p.Language() != "astro" {
		t.Errorf("Language = %q, want astro", p.Language())
	}

	symbols, imports, err := p.Parse("src/features/catalog/sample.astro", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{
		"const page",
		"const products",
		"func formatTitle",
		"type PageProps",
	}
	got := sortedSymbolKeys(symbols)
	if len(got) != len(want) {
		t.Fatalf("symbols = %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("symbols = %v\nwant %v", got, want)
			break
		}
	}

	// Frontmatter symbols keep host-file coordinates; so do script-block ones.
	if s := findSymbol(t, symbols, "PageProps", KindType); s.LineStart != 6 {
		t.Errorf("PageProps LineStart = %d, want 6", s.LineStart)
	}
	if s := findSymbol(t, symbols, "page", KindConst); s.LineStart != 28 {
		t.Errorf("page LineStart = %d, want 28 (script block after markup)", s.LineStart)
	}

	gotImports := sortedImportPaths(imports)
	wantImports := []string{
		"@features/catalog/lib/api",
		"src/features/layouts/BaseLayout",
		"src/features/lib/analytics",
		"src/features/types/product",
	}
	if len(gotImports) != len(wantImports) {
		t.Fatalf("imports = %v\nwant %v", gotImports, wantImports)
	}
	for i := range wantImports {
		if gotImports[i] != wantImports[i] {
			t.Errorf("imports = %v\nwant %v", gotImports, wantImports)
			break
		}
	}
}

func TestLocalImportPathTypeScript(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{"src/lib/foo.ts", "src/lib/foo"},
		{"src/lib/foo/index.ts", "src/lib/foo"},
		{"src/lib/comp.svelte", "src/lib/comp"},
		{"src/pages/index.astro", "src/pages"},
		{"scripts/tool.mjs", "scripts/tool"},
	} {
		got, err := LocalImportPath("/repo", tc.file)
		if err != nil {
			t.Errorf("LocalImportPath(%s): %v", tc.file, err)
			continue
		}
		if got != tc.want {
			t.Errorf("LocalImportPath(%s) = %q, want %q", tc.file, got, tc.want)
		}
	}

	if _, err := LocalImportPath("/repo", "styles.css"); err == nil {
		t.Error("expected error for .css")
	}
}

// TestTypeScriptSignatureBlanksStringContents pins the signature-rendering
// contract: string and template literal contents are blanked (no source
// strings leak into the index), but delimiters stay balanced — signatures
// must not dangle a quote or a '${'.
func TestTypeScriptSignatureBlanksStringContents(t *testing.T) {
	src := "export const VERSION = \"1.0\";\nconst tpl = `hi ${name} there`;\n"
	p := TypeScriptParser{}
	symbols, _, err := p.Parse("src/version.ts", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	version := findSymbol(t, symbols, "VERSION", KindConst)
	if want := `export const VERSION = "";`; version.Signature != want {
		t.Errorf("VERSION signature = %q, want %q", version.Signature, want)
	}
	tpl := findSymbol(t, symbols, "tpl", KindConst)
	if want := "const tpl = `${name}`;"; tpl.Signature != want {
		t.Errorf("tpl signature = %q, want %q", tpl.Signature, want)
	}
}
