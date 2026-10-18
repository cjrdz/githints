package index

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cjrdz/githints/internal/index/lang"
)

// TestScanCostModel pins what made a full scan slow, by counting rather than
// timing (a timing threshold flakes on a busy CI machine):
//
//   - a full scan's ignore checks are one git listing per pass, never one
//     process per directory (two thirds of a scan once) or per depth level,
//     and the walk never descends into an ignored directory;
//   - a rescan of an unchanged repository rewrites no note;
//   - the hook asks git about a commit's files in one batch, and looks up
//     import keys instead of recomputing every indexed file's.
func TestScanCostModel(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	// 4 levels: pkg/p<i>/sub<j>/leaf, 10 x 4 directories with files, and a
	// node_modules of 60 directories that must never be listed.
	for i := 0; i < 10; i++ {
		for j := 0; j < 4; j++ {
			d := filepath.Join(root, "pkg", fmt.Sprintf("p%d", i), fmt.Sprintf("sub%d", j), "leaf")
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			writeGo(t, root, filepath.Join("pkg", fmt.Sprintf("p%d", i), fmt.Sprintf("sub%d", j), "leaf", "f.go"),
				fmt.Sprintf("package leaf\n\nfunc F%d%d() {}\n", i, j))
		}
	}
	for i := 0; i < 60; i++ {
		if err := os.MkdirAll(filepath.Join(root, "node_modules", fmt.Sprintf("m%d", i), "lib"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGo(t, root, ".gitignore", "node_modules/\n")
	writeGo(t, root, ".githintsignore", "pkg/p9/\n")
	opts := lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}

	before := gitIgnoreCalls.Load()
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	// One listing per pass (.gitignore, .githintsignore), however deep the
	// tree: scanIgnores.
	if calls := gitIgnoreCalls.Load() - before; calls > 2 {
		t.Errorf("full scan started %d git ignore checks; want at most 2 (one listing per pass)", calls)
	}
	if n, _ := st.FileCount(); n != 36 {
		t.Errorf("indexed %d files, want 36 (pkg/p9 excluded by .githintsignore)", n)
	}

	// An unchanged rescan leaves every note's modification time alone.
	notes, _ := filepath.Glob(filepath.Join(root, ".githints", "index", "pkg", "*", "*", "leaf", "f.go.md"))
	if len(notes) != 36 {
		t.Fatalf("found %d notes, want 36", len(notes))
	}
	old := time.Now().Add(-time.Hour)
	for _, n := range notes {
		if err := os.Chtimes(n, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if fi, err := os.Stat(n); err != nil || !fi.ModTime().Equal(old) {
			t.Fatalf("%s was rewritten by an unchanged rescan", n)
		}
	}

	// The hook: one batch for thirty files, two passes.
	var paths []string
	for i := 0; i < 9; i++ {
		for j := 0; j < 4; j++ {
			paths = append(paths, fmt.Sprintf("pkg/p%d/sub%d/leaf/f.go", i, j))
		}
	}
	before = gitIgnoreCalls.Load()
	recomputes := keyRecomputes.Load()
	if err := IncrementalScan(st, opts, paths[:30], 0); err != nil {
		t.Fatal(err)
	}
	if calls := gitIgnoreCalls.Load() - before; calls > 2 {
		t.Errorf("the hook started %d git ignore checks for 30 files; want at most 2", calls)
	}
	// It looks keys up in file_keys rather than computing one for every
	// indexed file, which grew with the repository.
	if n := keyRecomputes.Load() - recomputes; n != 0 {
		t.Errorf("the hook recomputed every import key %d time(s); want 0", n)
	}
}

// TestPruneRemovesEmptiedNoteDirectories: when a source file goes, its note
// goes, and the note directories left empty go with it, up the tree; a
// directory still holding a note stays.
func TestPruneRemovesEmptiedNoteDirectories(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	for _, d := range []string{"a/b/c", "a/keep"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeGo(t, root, "a/b/c/gone.go", "package c\n\nfunc Gone() {}\n")
	writeGo(t, root, "a/keep/stay.go", "package keep\n\nfunc Stay() {}\n")
	opts := lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(root, ".githints", "index")
	if _, err := os.Stat(filepath.Join(notes, "a", "b", "c", "gone.go.md")); err != nil {
		t.Fatalf("note not written: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(notes, "a", "b")); !os.IsNotExist(err) {
		t.Errorf("emptied note directories remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(notes, "a", "keep", "stay.go.md")); err != nil {
		t.Errorf("a live note was removed: %v", err)
	}
}

// TestScanGCPacingIsScopedAndRestored: a scan raises GC pacing only while it
// runs, overlapping scans restore the original once the last ends, and a
// GOGC the user set is never overridden.
func TestScanGCPacingIsScopedAndRestored(t *testing.T) {
	t.Setenv("GOGC", "")
	orig := debug.SetGCPercent(100)
	defer debug.SetGCPercent(orig)

	beginScanGC()
	beginScanGC()
	if got := debug.SetGCPercent(scanGCPercent); got != scanGCPercent {
		t.Fatalf("during a scan GOGC = %d, want %d", got, scanGCPercent)
	}
	endScanGC()
	if got := debug.SetGCPercent(scanGCPercent); got != scanGCPercent {
		t.Fatalf("with one scan still running GOGC = %d, want %d", got, scanGCPercent)
	}
	endScanGC()
	if got := debug.SetGCPercent(100); got != 100 {
		t.Fatalf("after the last scan GOGC = %d, want the original 100", got)
	}

	t.Setenv("GOGC", "50")
	debug.SetGCPercent(50)
	beginScanGC()
	if got := debug.SetGCPercent(50); got != 50 {
		t.Errorf("a user-set GOGC was overridden: %d", got)
	}
	endScanGC()
}

// TestScanMemoryLimitIsScopedAndRestored: a scan sets a soft memory limit
// and restores the previous one, leaves a user's GOMEMLIMIT alone, and never
// raises a lower limit already in force.
func TestScanMemoryLimitIsScopedAndRestored(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	orig := debug.SetMemoryLimit(math.MaxInt64)
	defer debug.SetMemoryLimit(orig)

	beginScanGC()
	if got := debug.SetMemoryLimit(-1); got != scanMemoryLimit {
		t.Fatalf("during a scan the limit is %d, want %d", got, scanMemoryLimit)
	}
	endScanGC()
	if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
		t.Fatalf("after the scan the limit is %d, want it restored", got)
	}

	debug.SetMemoryLimit(64 << 20)
	beginScanGC()
	if got := debug.SetMemoryLimit(-1); got != 64<<20 {
		t.Errorf("a lower limit was raised to %d", got)
	}
	endScanGC()

	t.Setenv("GOMEMLIMIT", "1GiB")
	debug.SetMemoryLimit(1 << 30)
	beginScanGC()
	if got := debug.SetMemoryLimit(-1); got != 1<<30 {
		t.Errorf("a user-set GOMEMLIMIT was overridden: %d", got)
	}
	endScanGC()
}

// TestInParallelRunsEveryIndexOnce: every index runs exactly once whatever
// the worker count, including more workers than items and none at all.
func TestInParallelRunsEveryIndexOnce(t *testing.T) {
	for _, c := range []struct{ n, workers int }{{0, 4}, {1, 4}, {3, 12}, {1000, 4}, {1000, 1}} {
		hits := make([]int32, c.n)
		inParallel(c.n, c.workers, func(i int) { atomic.AddInt32(&hits[i], 1) })
		for i, h := range hits {
			if h != 1 {
				t.Fatalf("n=%d workers=%d: index %d ran %d times", c.n, c.workers, i, h)
			}
		}
	}
}

// TestPruneDoesNotDescendIntoALinkedNoteDirectory: a link inside
// .githints/index to a directory elsewhere is never walked, so the .md files
// over there are never pruned; a stale note linked in as a file is removed as
// a link, leaving its target alone.
func TestPruneDoesNotDescendIntoALinkedNoteDirectory(t *testing.T) {
	st, dir := tempStore(t)
	defer st.Close()
	root := filepath.Join(dir, "repo")
	initGitRepo(t, root)
	writeGo(t, root, "main.go", "package main\n\nfunc main() {}\n")
	opts := lang.ScanOptions{Root: root, Languages: []string{"go"}, MaxFileSize: 1 << 20, ParseTimeout: 5 * time.Second}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "precious.md")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(root, ".githints", "index")
	if err := os.Symlink(outside, filepath.Join(notes, "elsewhere")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(victim, filepath.Join(notes, "stale.md")); err != nil {
		t.Fatal(err)
	}
	if err := FullScan(st, opts, true, 0); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep me" {
		t.Fatalf("a file behind a link was touched: %q, %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(notes, "stale.md")); !os.IsNotExist(err) {
		t.Errorf("the stale linked note was not removed: %v", err)
	}
}
