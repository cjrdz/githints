package safefs

import (
	"errors"
	"os"
	"path/filepath"
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
