package index

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
)

func tempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st, dir
}

func TestStoreRoundTrip(t *testing.T) {
	st, _ := tempStore(t)
	defer st.Close()

	if err := st.InsertSymbols([]lang.Symbol{
		{Name: "A", Kind: lang.KindFunc, FilePath: "a.go", LineStart: 1, LineEnd: 2},
		{Name: "B", Kind: lang.KindType, FilePath: "a.go", LineStart: 4, LineEnd: 5},
		{Name: "C", Kind: lang.KindFunc, FilePath: "b.go", LineStart: 1, LineEnd: 3},
	}); err != nil {
		t.Fatalf("InsertSymbols: %v", err)
	}
	if err := st.InsertImports([]lang.Import{
		{FilePath: "b.go", ImportedPath: "a.go"},
	}); err != nil {
		t.Fatalf("InsertImports: %v", err)
	}

	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
	if n, err := st.ImportCount(); err != nil || n != 1 {
		t.Fatalf("ImportCount = %d, %v", n, err)
	}
	if n, err := st.FileCount(); err != nil || n != 2 {
		t.Fatalf("FileCount = %d, %v", n, err)
	}

	files, err := st.AllIndexedFiles()
	if err != nil {
		t.Fatalf("AllIndexedFiles: %v", err)
	}
	if len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Errorf("AllIndexedFiles = %v", files)
	}

	syms, err := st.SymbolsForFile("a.go")
	if err != nil {
		t.Fatalf("SymbolsForFile: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("SymbolsForFile = %d", len(syms))
	}
	if syms[0].Name != "A" || syms[1].Name != "B" {
		t.Errorf("names = %v", syms)
	}

	matches, err := st.FindSymbolsByName("A")
	if err != nil {
		t.Fatalf("FindSymbolsByName: %v", err)
	}
	if len(matches) != 1 || matches[0].Name != "A" {
		t.Errorf("FindSymbolsByName(A) = %v", matches)
	}

	dependents, err := st.FilesImporting("a.go")
	if err != nil {
		t.Fatalf("FilesImporting: %v", err)
	}
	if len(dependents) != 1 || dependents[0].FilePath != "b.go" {
		t.Errorf("FilesImporting = %v", dependents)
	}

	hubs, err := st.TopFilesByInDegree(10)
	if err != nil {
		t.Fatalf("TopFilesByInDegree: %v", err)
	}
	if len(hubs) != 1 || hubs[0].File != "a.go" || hubs[0].Dependents != 1 {
		t.Errorf("TopFilesByInDegree = %v", hubs)
	}
}

func TestStoreClearAndDelete(t *testing.T) {
	st, _ := tempStore(t)
	defer st.Close()

	if err := st.InsertSymbols([]lang.Symbol{
		{Name: "A", Kind: lang.KindFunc, FilePath: "a.go", LineStart: 1, LineEnd: 2},
	}); err != nil {
		t.Fatalf("InsertSymbols: %v", err)
	}
	if err := st.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 0 {
		t.Fatalf("SymbolCount after Clear = %d", n)
	}

	if err := st.InsertSymbols([]lang.Symbol{
		{Name: "A", Kind: lang.KindFunc, FilePath: "a.go", LineStart: 1, LineEnd: 2},
		{Name: "B", Kind: lang.KindFunc, FilePath: "b.go", LineStart: 1, LineEnd: 2},
	}); err != nil {
		t.Fatalf("InsertSymbols: %v", err)
	}
	if err := st.DeleteFile("a.go"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if n, err := st.FileCount(); err != nil || n != 1 {
		t.Fatalf("FileCount after delete = %d", n)
	}
}

func TestStoreMeta(t *testing.T) {
	st, _ := tempStore(t)
	defer st.Close()

	if err := st.SetMeta(lang.IndexMeta{
		LastIndexedAt:    123,
		FileCount:        5,
		SymbolCount:      10,
		LanguageCounts:   map[string]int{"go": 5},
		SkippedCount:     1,
		UnsupportedCount: 2,
	}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}

	meta, err := st.Meta()
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.LastIndexedAt != 123 {
		t.Errorf("LastIndexedAt = %d", meta.LastIndexedAt)
	}
	if meta.FileCount != 5 || meta.SymbolCount != 10 {
		t.Errorf("counts = %d, %d", meta.FileCount, meta.SymbolCount)
	}
	if meta.LanguageCounts["go"] != 5 {
		t.Errorf("LanguageCounts = %v", meta.LanguageCounts)
	}
	if meta.SkippedCount != 1 || meta.UnsupportedCount != 2 {
		t.Errorf("skipped/unsupported = %d, %d", meta.SkippedCount, meta.UnsupportedCount)
	}
}

func TestFullScanIndexesGo(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "main.go"), []byte(`package main

import "fmt"

func main() {
	fmt.Println("hello")
}

type App struct{}

func (a *App) Run() error { return nil }
`), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	// Initialize a git repo so check-ignore works.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatalf("mkdir refs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n\trepositoryformatversion = 0\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs"), 0o755); err != nil {
		t.Fatalf("mkdir refs: %v", err)
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
	if n, err := st.FileCount(); err != nil || n != 1 {
		t.Fatalf("FileCount = %d, %v", n, err)
	}

	meta, err := st.Meta()
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.LanguageCounts["go"] != 1 {
		t.Errorf("LanguageCounts = %v", meta.LanguageCounts)
	}

	// Check that the note file exists.
	note := lang.NotePath(root, "cmd/main.go")
	if _, err := os.Stat(note); err != nil {
		t.Errorf("note missing: %v", err)
	}
	roll := lang.IndexRollupPath(root)
	if _, err := os.Stat(roll); err != nil {
		t.Errorf("rollup missing: %v", err)
	}

	// Verify idempotency: run again and check the symbol count is the same.
	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan second run: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("SymbolCount after second run = %d, %v", n, err)
	}
}

// TestFullScanRendersImportedBy proves the per-file note's "Imported by"
// section reflects real importers. Dependents are stored by import path (Go
// module path, normalized TS file key), not by file path; the renderer used
// to query by raw file path, which silently matched nothing, so the section
// never appeared for any language.
func TestFullScanRendersImportedBy(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.23\n")
	write("pkg/lib/lib.go", "package lib\n\nfunc Helper() {}\n")
	write("cmd/main.go", "package main\n\nimport \"example.com/m/pkg/lib\"\n\nfunc main() { lib.Helper() }\n")
	write("src/helper.ts", "export function helper(name: string): string {\n  return name;\n}\n")
	write("src/main.ts", "import { helper } from \"./helper\";\n\nexport const msg = helper(\"x\");\n")
	// Minimal .git so check-ignore works (same fixture as TestFullScanIndexesGo).
	write(".git/HEAD", "ref: refs/heads/main\n")
	write(".git/config", "[core]\n\trepositoryformatversion = 0\n")
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs"), 0o755); err != nil {
		t.Fatalf("mkdir refs: %v", err)
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go", "typescript"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(lang.NotePath(root, rel))
		if err != nil {
			t.Fatalf("read note %s: %v", rel, err)
		}
		return string(data)
	}

	goNote := read("pkg/lib/lib.go")
	if !strings.Contains(goNote, "## Imported by") || !strings.Contains(goNote, "cmd/main.go") {
		t.Errorf("Go note missing Imported by section:\n%s", goNote)
	}
	tsNote := read("src/helper.ts")
	if !strings.Contains(tsNote, "## Imported by") || !strings.Contains(tsNote, "src/main.ts") {
		t.Errorf("TS note missing Imported by section:\n%s", tsNote)
	}
}

func TestFullScanSkipsIgnoredFiles(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "tracked.go", "package main\nfunc Tracked() {}\n")
	writeGo(t, root, "ignored.go", "package main\nfunc Ignored() {}\n")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	if n, err := st.SymbolCount(); err != nil || n != 1 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
	files, _ := st.AllIndexedFiles()
	if len(files) != 1 || files[0] != "tracked.go" {
		t.Errorf("files = %v", files)
	}
}

func TestFullScanRespectsGithintsignore(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "tracked.go", "package main\nfunc Tracked() {}\n")
	writeGo(t, root, "generated.go", "package main\nfunc Generated() {}\n")
	if err := os.WriteFile(filepath.Join(root, ".githintsignore"), []byte("generated.go\n"), 0o644); err != nil {
		t.Fatalf("write .githintsignore: %v", err)
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	if n, err := st.SymbolCount(); err != nil || n != 1 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
	files, _ := st.AllIndexedFiles()
	if len(files) != 1 || files[0] != "tracked.go" {
		t.Errorf("files = %v", files)
	}
}

func TestFullScanGithintsignoreCannotReincludeGitignored(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "tracked.go", "package main\nfunc Tracked() {}\n")
	writeGo(t, root, "ignored.go", "package main\nfunc Ignored() {}\n")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	// .githintsignore tries to re-include ignored.go — must not work.
	if err := os.WriteFile(filepath.Join(root, ".githintsignore"), []byte("!ignored.go\n"), 0o644); err != nil {
		t.Fatalf("write .githintsignore: %v", err)
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	files, _ := st.AllIndexedFiles()
	if len(files) != 1 || files[0] != "tracked.go" {
		t.Errorf("files = %v", files)
	}
}

func TestFullScanSkipsSymlink(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "real.go", "package main\nfunc Real() {}\n")
	if err := os.Symlink(filepath.Join(root, "real.go"), filepath.Join(root, "link.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 1 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
}

func TestFullScanMaxFileSize(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "small.go", "package main\nfunc Small() {}\n")
	big := "package main\n" + strings.Repeat("// big\n", 1000) + "func Big() {}\n"
	writeGo(t, root, "big.go", big)

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 64, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 1 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
	files, _ := st.AllIndexedFiles()
	if len(files) != 1 || files[0] != "small.go" {
		t.Errorf("files = %v", files)
	}
	meta, _ := st.Meta()
	if meta.SkippedCount != 1 {
		t.Errorf("SkippedCount = %d", meta.SkippedCount)
	}
}

func TestFullScanPartialWriteGuard(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "a.go", "package main\nfunc A() {}\nfunc B() {}\n")
	writeGo(t, root, "b.go", "package main\nfunc C() {}\n")

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("initial FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("initial SymbolCount = %d, %v", n, err)
	}

	// Delete one file, then refuse to overwrite without force.
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatalf("remove b.go: %v", err)
	}
	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err == nil {
		t.Fatal("expected partial-write error")
	}
	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("SymbolCount after refused scan = %d, %v", n, err)
	}

	// Force overwrites.
	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, true, 0); err != nil {
		t.Fatalf("forced FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 2 {
		t.Fatalf("SymbolCount after forced scan = %d, %v", n, err)
	}
}

func TestFullScanMaxBytesGuard(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "a.go", "package main\nfunc A() {}\n")

	// A tiny maxBytes always refuses because the empty SQLite file is larger than
	// the allowance, unless force overrides it.
	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 100); err == nil {
		t.Fatal("expected max_bytes error")
	}

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, true, 100); err != nil {
		t.Fatalf("forced FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 1 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}
}

func TestFullScanObsidianWikilinks(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	// The renderer resolves a file to its module import path before looking up
	// dependents, so the fixture needs a go.mod and a real module-path import.
	writeGo(t, root, "go.mod", "module example.com/m\n\ngo 1.23\n")
	writeGo(t, root, "a.go", "package main\nfunc A() {}\n")
	writeGo(t, root, "b.go", "package main\nimport \"example.com/m\"\nfunc B() {}\n")

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second, Obsidian: true}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	noteA := lang.NotePath(root, "a.go")
	data, err := os.ReadFile(noteA)
	if err != nil {
		t.Fatalf("read note a.go: %v", err)
	}
	if !strings.Contains(string(data), "[[b.go.md|b.go]]") {
		t.Errorf("expected Obsidian wikilink to b.go, got:\n%s", string(data))
	}
}

func TestFullScanObsidianEscapesBracketsInFilename(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	// File name with brackets breaks wikilink syntax; the target must be URL-encoded.
	writeGo(t, root, "go.mod", "module example.com/m\n\ngo 1.23\n")
	writeGo(t, root, "a.go", "package main\nfunc A() {}\n")
	writeGo(t, root, "file[weird].go", "package main\nimport \"example.com/m\"\nfunc Weird() {}\n")

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second, Obsidian: true}, false, 0); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	noteA := lang.NotePath(root, "a.go")
	data, err := os.ReadFile(noteA)
	if err != nil {
		t.Fatalf("read note a.go: %v", err)
	}
	if !strings.Contains(string(data), "[[file%5Bweird%5D.go.md|file[weird].go]]") {
		t.Errorf("expected URL-encoded target with raw display text, got:\n%s", string(data))
	}
}

func TestFileLinkDefaultIsMarkdown(t *testing.T) {
	got := lang.FileLink("/repo", "cmd/main.go", false)
	want := "[cmd/main.go](cmd/main.go)"
	if got != want {
		t.Errorf("FileLink default = %q, want %q", got, want)
	}
}

func TestFileLinkObsidianWikilink(t *testing.T) {
	got := lang.FileLink("/repo", "cmd/main.go", true)
	want := "[[cmd/main.go.md|cmd/main.go]]"
	if got != want {
		t.Errorf("FileLink obsidian = %q, want %q", got, want)
	}
}

func TestFileLinkObsidianEscapesBracketsAndPipe(t *testing.T) {
	got := lang.FileLink("/repo", "file[weird|name].go", true)
	want := "[[file%5Bweird%7Cname%5D.go.md|file[weird|name].go]]"
	if got != want {
		t.Errorf("FileLink obsidian escape = %q, want %q", got, want)
	}
}

func TestIncrementalScanUpdatesOnlyChangedFiles(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "a.go", "package main\nfunc A() {}\n")
	writeGo(t, root, "b.go", "package main\nfunc B() {}\n")

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, false, 0); err != nil {
		t.Fatalf("initial FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 2 {
		t.Fatalf("initial SymbolCount = %d, %v", n, err)
	}

	// Modify b.go and delete a.go.
	writeGo(t, root, "b.go", "package main\nfunc B() {}\nfunc B2() {}\n")
	if err := os.Remove(filepath.Join(root, "a.go")); err != nil {
		t.Fatalf("remove a.go: %v", err)
	}
	if err := IncrementalScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, []string{"a.go", "b.go"}); err != nil {
		t.Fatalf("IncrementalScan: %v", err)
	}

	symsA, err := st.SymbolsForFile("a.go")
	if err != nil {
		t.Fatalf("SymbolsForFile a.go: %v", err)
	}
	if len(symsA) != 0 {
		t.Errorf("a.go should have been deleted, symbols = %v", symsA)
	}
	symsB, err := st.SymbolsForFile("b.go")
	if err != nil {
		t.Fatalf("SymbolsForFile b.go: %v", err)
	}
	if len(symsB) != 2 {
		t.Errorf("b.go symbols = %d, want 2", len(symsB))
	}

	meta, err := st.Meta()
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if meta.LastIndexedAt == 0 {
		t.Error("LastIndexedAt should be refreshed")
	}
}

func initGitRepo(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "config", "-f", filepath.Join(root, ".git", "config"), "user.email", "test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
}

func writeGo(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
