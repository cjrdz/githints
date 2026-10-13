package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cjrdz/githints/internal/cli/core"
	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/gitutil"
	"github.com/cjrdz/githints/internal/integrity"
	"github.com/cjrdz/githints/internal/recorder"
	"github.com/cjrdz/githints/internal/store"
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

func TestHookRunSkipsFallbackWhenAgentDescribedFile(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	disableInstalledHooks(t, dir)

	// First commit: create a tracked file.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", "main.go")
	clitest.RunGit(t, dir, "commit", "-m", "initial")

	// Modify and record an agent change before committing.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordAgent(t, "main.go", "add main function")
	clitest.RunGit(t, dir, "add", "main.go")
	clitest.RunGit(t, dir, "commit", "-m", "second")

	if err := HookRun(); err != nil {
		t.Fatalf("HookRun: %v", err)
	}

	rows := commitRows(t, dir)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Source != "agent" {
		t.Errorf("source = %q, want agent", rows[0].Source)
	}
}

func TestHookRunWritesFallbackWhenAgentDidNotDescribeFile(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}
	disableInstalledHooks(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", "main.go")
	clitest.RunGit(t, dir, "commit", "-m", "initial")

	// Modify without recording an agent change.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clitest.RunGit(t, dir, "add", "main.go")
	clitest.RunGit(t, dir, "commit", "-m", "second")

	if err := HookRun(); err != nil {
		t.Fatalf("HookRun: %v", err)
	}

	rows := commitRows(t, dir)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Source != "fallback" {
		t.Errorf("source = %q, want fallback", rows[0].Source)
	}
}

func disableInstalledHooks(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"post-commit", "pre-commit"} {
		path := filepath.Join(dir, ".git", "hooks", name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
}

func recordAgent(t *testing.T, file, summary string) {
	t.Helper()
	root, st, err := core.OpenInitialized()
	if err != nil {
		t.Fatalf("OpenInitialized: %v", err)
	}
	defer st.Close()
	key, err := integrity.KeyFromRepo(root)
	if err != nil {
		t.Fatalf("KeyFromRepo: %v", err)
	}
	if err := recorder.Record(st, root, key, recorder.Input{
		FilePath: file,
		Summary:  summary,
		Source:   "agent",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func commitRows(t *testing.T, dir string) []store.Change {
	t.Helper()
	hash, err := gitutil.LastCommitHash()
	if err != nil {
		t.Fatalf("LastCommitHash: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer st.Close()
	rows, err := st.ChangesForCommit(hash)
	if err != nil {
		t.Fatalf("ChangesForCommit: %v", err)
	}
	return rows
}
