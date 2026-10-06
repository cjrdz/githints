//go:build unix

package safefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadFileRefusesNonRegular(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Opening a FIFO with no writer would block forever; the check has to
	// happen before Open.
	if _, err := ReadFile(dir, "fifo", 1024); err == nil {
		t.Fatal("ReadFile accepted a FIFO")
	}
	if _, err := ReadFile(dir, ".", 1024); err == nil {
		t.Fatal("ReadFile accepted a directory")
	}
}

// Private-mode summaries were world-readable on a shared host: .githints was
// 0755 and SQLite created the database (and its WAL and SHM) with the umask.
func TestPrepareDatabaseRestrictsModes(t *testing.T) {
	syscall.Umask(0o022)
	dir := filepath.Join(t.TempDir(), ".githints")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(dir, "old.db")
	if err := os.WriteFile(loose, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "new.db"), loose} {
		if err := PrepareDatabase(p); err != nil {
			t.Fatalf("PrepareDatabase(%s): %v", p, err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", p, fi.Mode().Perm())
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf(".githints mode = %o, want 700", fi.Mode().Perm())
	}
}
