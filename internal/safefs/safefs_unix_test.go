//go:build unix

package safefs

import (
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
