package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureDirRefusesLink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a directory symlink: %v", err)
	}
	if err := EnsureDir(link, 0o755); err == nil {
		t.Fatal("EnsureDir accepted a symlinked directory")
	}
	if err := WriteFile(link, "x.md", []byte("x"), 0o755, 0o644); err == nil {
		t.Fatal("WriteFile wrote through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(target, "x.md")); err == nil {
		t.Fatal("file landed in the link target")
	}
}

func TestWriteFileRefusesRelativeEscape(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(dir, "up")); err != nil {
		t.Skipf("cannot create a relative symlink: %v", err)
	}
	if err := WriteFile(dir, "up/escaped.md", []byte("x"), 0o755, 0o644); err == nil {
		t.Fatal("WriteFile followed a relative link out of the directory")
	}
	if _, err := os.Stat(filepath.Join(base, "escaped.md")); err == nil {
		t.Fatal("file landed outside the directory")
	}
}

func TestReadFileCapsSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(dir, "big", 99); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadFile over cap: err = %v, want ErrTooLarge", err)
	}
	got, err := ReadFile(dir, "big", 100)
	if err != nil || len(got) != 100 {
		t.Fatalf("ReadFile at cap: %d bytes, %v", len(got), err)
	}
}

func TestReadFileRefusesAbsoluteLinkOut(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "leak")); err != nil {
		t.Skipf("cannot create a file symlink: %v", err)
	}
	if _, err := ReadFile(dir, "leak", 1024); err == nil {
		t.Fatal("ReadFile followed a link out of the directory")
	}
}

// os.Root follows a link that stays inside the root, so a link planted in the
// directory used to turn a routine rewrite of a rendered file into an overwrite
// of something else in there: `CHANGES.md -> store.db` destroyed the change log.
func TestWriteFileRefusesToWriteThroughALink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "store.db")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("store.db", filepath.Join(dir, "CHANGES.md")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	os.Symlink("nowhere-at-all", filepath.Join(dir, "dangling.md"))
	os.Symlink("real", filepath.Join(dir, "linked-dir"))
	os.Symlink("..", filepath.Join(dir, "up"))

	for name, rel := range map[string]string{
		"link to a file":   "CHANGES.md",
		"dangling link":    "dangling.md",
		"link as a parent": "linked-dir/x.md",
		"link to a parent": "up/x.md",
		"nested link":      "linked-dir/deeper/x.md",
	} {
		if err := WriteFile(dir, rel, []byte("overwritten"), 0o755, 0o644); err == nil {
			t.Errorf("%s: wrote through the link", name)
		}
	}
	if b, _ := os.ReadFile(victim); string(b) != "ORIGINAL" {
		t.Errorf("the file behind a link was changed: %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "real")); len(entries) != 0 {
		t.Errorf("a file landed in the directory a link points at: %v", entries)
	}
}

func TestWriteFileStillWritesOrdinaryFiles(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"a.md", "sub/b.md", "sub/deeper/c.md", "a.md"} { // a.md twice: overwrite
		if err := WriteFile(dir, rel, []byte("x "+rel), 0o755, 0o644); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "sub", "deeper", "c.md")); string(b) != "x sub/deeper/c.md" {
		t.Errorf("nested write: %q", b)
	}
}

// A hard link is a regular file, so no Lstat can tell it from any other: a second
// name for a state file reads like an ordinary file of whatever name it is given.
func TestReadSingleLinkFileRefusesAHardLink(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(orig, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if b, err := ReadSingleLinkFileIn(r, "secret.txt", 100); err != nil || string(b) != "SECRET" {
		t.Fatalf("a file with one name: %q, %v", b, err)
	}
	if err := os.Link(orig, filepath.Join(dir, "alias.md")); err != nil {
		t.Skipf("cannot create hard links here: %v", err)
	}
	for _, name := range []string{"alias.md", "secret.txt"} { // both names of the same file
		if _, err := ReadSingleLinkFileIn(r, name, 100); err == nil {
			t.Errorf("read %s, which has two names", name)
		}
	}
	// The plain reader is unchanged: only callers that ask for the check get it.
	if b, err := ReadFileIn(r, "alias.md", 100); err != nil || string(b) != "SECRET" {
		t.Errorf("ReadFileIn changed behavior: %q, %v", b, err)
	}
}

// TestWriterRefusesLinksLikeWriteFile: the pass writer keeps WriteFileIn's
// guarantee, including for a link whose target already holds the bytes
// being written, which the unchanged-file check must not short-circuit.
func TestWriterRefusesLinksLikeWriteFile(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "store.db")
	if err := os.WriteFile(victim, []byte("overwritten"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("store.db", filepath.Join(dir, "CHANGES.md")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	os.Symlink("nowhere-at-all", filepath.Join(dir, "dangling.md"))
	os.Symlink("real", filepath.Join(dir, "linked-dir"))
	os.Symlink("..", filepath.Join(dir, "up"))

	w, err := NewWriter(dir, 0o755, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for name, rel := range map[string]string{
		"link to a file with the same bytes": "CHANGES.md",
		"dangling link":                      "dangling.md",
		"link as a parent":                   "linked-dir/x.md",
		"link to a parent":                   "up/x.md",
		"nested link":                        "linked-dir/deeper/x.md",
	} {
		if err := w.WriteFile(rel, []byte("overwritten")); err == nil {
			t.Errorf("%s: wrote through the link", name)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "real")); len(entries) != 0 {
		t.Errorf("a file landed in the directory a link points at: %v", entries)
	}
	if w.Written() != 0 || w.Unchanged() != 0 {
		t.Errorf("counted refused writes: written %d, unchanged %d", w.Written(), w.Unchanged())
	}
}

func TestWriterSkipsUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, 0o755, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, step := range []struct{ rel, data string }{
		{"index/a.md", "one"},
		{"index/deep/b.md", "two"},
		{"index/a.md", "one"}, // unchanged
		{"index/a.md", "uno"}, // same size, new content
		{"index/a.md", "longer"},
	} {
		if err := w.WriteFile(step.rel, []byte(step.data)); err != nil {
			t.Fatalf("%s: %v", step.rel, err)
		}
	}
	if w.Written() != 4 || w.Unchanged() != 1 {
		t.Errorf("written %d, unchanged %d; want 4 and 1", w.Written(), w.Unchanged())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "index", "a.md")); string(b) != "longer" {
		t.Errorf("a.md = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "index", "deep", "b.md")); string(b) != "two" {
		t.Errorf("b.md = %q", b)
	}
}

// TestWriterIsSafeForConcurrentUse: many goroutines writing into the same new
// directories (run under -race), with every file written exactly once and
// identical rewrites counted as unchanged.
func TestWriterIsSafeForConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, 0o755, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	const n = 200
	var wg sync.WaitGroup
	for round := 0; round < 2; round++ {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				rel := fmt.Sprintf("index/d%d/e%d/f%d.md", i%7, i%3, i)
				if err := w.WriteFile(rel, []byte(rel)); err != nil {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
	}
	if w.Written() != n || w.Unchanged() != n {
		t.Errorf("written %d, unchanged %d; want %d each", w.Written(), w.Unchanged(), n)
	}
}
