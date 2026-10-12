package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
)

func TestPreCommitNeverBlocksOnInternalError(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	t.Setenv("GITHINTS_PRECOMMIT_BLOCK", "1")
	dir := clitest.ChdirTempRepo(t)
	// A regular file where the .githints directory should be.
	if err := os.WriteFile(filepath.Join(dir, ".githints"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", "a.go")
	if err := PreCommit(); err != nil {
		t.Fatalf("pre-commit failed the commit on an internal error: %v", err)
	}
}

// Blocking mode still blocks for what it is for: staged files with no record.
func TestPreCommitBlocksUnrecordedFilesWhenAsked(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	t.Setenv("GITHINTS_PRECOMMIT_BLOCK", "1")
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", "a.go")
	if err := PreCommit(); err == nil {
		t.Fatal("blocking mode let an unrecorded file through")
	}
}

// init wrote to <root>/.git/hooks unconditionally, so with core.hooksPath set
// the hooks were installed where git never looks and silently never ran.
