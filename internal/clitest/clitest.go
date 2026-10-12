// Package clitest provides helpers used by tests across the CLI subpackages.
// It is only imported by _test.go files, so it does not ship in the binary.
package clitest

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// cwdMu serializes tests that change the process working directory.
// go test runs packages concurrently, and ChdirTempRepo mutates the global CWD.
var cwdMu sync.Mutex

// ChdirTempRepo creates a temporary git repository, changes into it, and
// returns the repository path. The working directory is restored at test end.
func ChdirTempRepo(t *testing.T) string {
	t.Helper()
	cwdMu.Lock()
	t.Cleanup(cwdMu.Unlock)

	dir := t.TempDir()
	RunGit(t, dir, "init")
	RunGit(t, dir, "config", "user.email", "test@example.com")
	// CI machines have no global identity; commits in tests need one.
	RunGit(t, dir, "config", "user.name", "Test User")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})
	return dir
}

// RunGit runs git with the given arguments in dir.
func RunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
