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

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
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
	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
		t.Fatalf("FullScan second run: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("SymbolCount after second run = %d, %v", n, err)
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

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
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

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
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

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
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

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
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

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 64, ParseTimeout: 5 * time.Second}); err != nil {
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

func TestIncrementalScanOnlyTouchesSubset(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "a.go", "package main\nfunc A() {}\n")
	writeGo(t, root, "b.go", "package main\nfunc B() {}\n")

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	if n, err := st.SymbolCount(); err != nil || n != 2 {
		t.Fatalf("SymbolCount = %d, %v", n, err)
	}

	// Capture a.go's note modification time.
	aNote := lang.NotePath(root, "a.go")
	aInfoBefore, err := os.Stat(aNote)
	if err != nil {
		t.Fatalf("stat a note before: %v", err)
	}

	// Modify only b.go.
	writeGo(t, root, "b.go", "package main\nfunc B() {}\nfunc B2() {}\n")
	if err := IncrementalScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, []string{"b.go"}); err != nil {
		t.Fatalf("IncrementalScan: %v", err)
	}

	if n, err := st.SymbolCount(); err != nil || n != 3 {
		t.Fatalf("SymbolCount after incremental = %d, %v", n, err)
	}
	files, _ := st.AllIndexedFiles()
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}

	// a.go's note should not have been re-rendered.
	aInfoAfter, err := os.Stat(aNote)
	if err != nil {
		t.Fatalf("stat a note after: %v", err)
	}
	if aInfoAfter.ModTime() != aInfoBefore.ModTime() {
		t.Errorf("a.go note was re-rendered even though only b.go changed")
	}

	// b.go's note should exist and reflect the new symbol.
	bNote := lang.NotePath(root, "b.go")
	bData, err := os.ReadFile(bNote)
	if err != nil {
		t.Fatalf("read b note: %v", err)
	}
	if !strings.Contains(string(bData), "B2") {
		t.Errorf("b.go note does not contain B2:\n%s", bData)
	}

	// INDEX.md should have been refreshed.
	rollInfo, err := os.Stat(lang.IndexRollupPath(root))
	if err != nil {
		t.Fatalf("stat rollup: %v", err)
	}
	if rollInfo.ModTime().Before(aInfoAfter.ModTime()) {
		t.Error("rollup was not refreshed after incremental scan")
	}
}

func TestIncrementalScanHandlesDeletion(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()

	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "a.go", "package main\nfunc A() {}\n")
	writeGo(t, root, "b.go", "package main\nfunc B() {}\n")

	if err := FullScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}); err != nil {
		t.Fatalf("FullScan: %v", err)
	}
	bNote := lang.NotePath(root, "b.go")
	if _, err := os.Stat(bNote); err != nil {
		t.Fatalf("b note should exist before deletion: %v", err)
	}

	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatalf("remove b.go: %v", err)
	}
	if err := IncrementalScan(st, lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1024, ParseTimeout: 5 * time.Second}, []string{"b.go"}); err != nil {
		t.Fatalf("IncrementalScan: %v", err)
	}

	if n, err := st.SymbolCount(); err != nil || n != 1 {
		t.Fatalf("SymbolCount after deletion = %d, %v", n, err)
	}
	files, _ := st.AllIndexedFiles()
	if len(files) != 1 || files[0] != "a.go" {
		t.Errorf("files = %v", files)
	}
	if _, err := os.Stat(bNote); !os.IsNotExist(err) {
		t.Errorf("b.go note still exists after deletion: %v", err)
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
