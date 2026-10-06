package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/store"
)

// Claude Code reads CLAUDE.md and does not read AGENTS.md, so both have to
// exist for the rules to reach every client.
func TestInitWritesAgentFiles(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)

	if err := cmdInit([]string{}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}

	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	for _, want := range []string{"record_change", "get_session_context", "get_file_history"} {
		if !strings.Contains(string(agents), want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, agents)
		}
	}

	claude, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claude), "@AGENTS.md") {
		t.Errorf("CLAUDE.md should import AGENTS.md:\n%s", claude)
	}
}

// Re-running init must update only its own block.
func TestInitPreservesHandWrittenContent(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)

	handWritten := "# My project\n\nDo not delete this line.\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(handWritten), 0o644); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := cmdInit([]string{"-force"}); err != nil {
			t.Fatalf("cmdInit run %d: %v", i, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	body := string(got)
	if !strings.Contains(body, "Do not delete this line.") {
		t.Errorf("init clobbered hand-written content:\n%s", body)
	}
	if n := strings.Count(body, mdManagedStart); n != 1 {
		t.Errorf("expected exactly 1 managed block after 2 runs, got %d:\n%s", n, body)
	}
}

// The error from ReadFile used to be discarded, so any failure other than
// not-exist produced an empty buffer and the write replaced the user's file
// with nothing but our own block.
func TestEnsureManagedBlockFailsClosedOnReadError(t *testing.T) {
	dir := t.TempDir()
	// A directory is readable by Stat but not by ReadFile, which gives us a
	// non-NotExist error on every platform.
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := ensureManagedBlock(path, mdManagedStart, mdManagedEnd, []string{"body"}); err == nil {
		t.Fatal("expected an error rather than a silent overwrite")
	}
}

// A brand-new file should not open with blank lines before the marker.
func TestEnsureManagedBlockNewFileHasNoLeadingBlanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := ensureManagedBlock(path, mdManagedStart, mdManagedEnd, claudeBlock); err != nil {
		t.Fatalf("ensureManagedBlock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasPrefix(string(data), mdManagedStart) {
		t.Errorf("new file should start with the marker, got:\n%q", string(data))
	}
}

// TestHookSurvivesBinaryMoving pins the fallback that makes package managers
// usable. os.Executable resolves symlinks, so `githints init` records the
// versioned target a Homebrew or Scoop shim points at, not the stable shim.
// Upgrading replaces that directory, and without a fallback every hook in
// every tracked repo would point at a deleted file and silently stop
// recording.
func TestHookSurvivesBinaryMoving(t *testing.T) {
	dir := t.TempDir()
	recorded := filepath.Join(dir, "versioned", "githints")

	hook := hookScriptFor(recorded, "hook-run")

	// The recorded path wins while it exists, so a local dev build is not
	// displaced by an unrelated githints on PATH.
	// The path is held in a shell variable, single-quoted (see
	// TestHookScriptQuotesHostilePath), and tested before anything else.
	if !strings.Contains(hook, `githints_bin='`+filepath.ToSlash(recorded)+`'`) ||
		!strings.Contains(hook, `[ -x "$githints_bin" ]`) {
		t.Errorf("hook does not test the recorded path first:\n%s", hook)
	}
	idxRecorded := strings.Index(hook, `[ -x "$githints_bin" ]`)
	idxPath := strings.Index(hook, "command -v githints")
	if idxRecorded < 0 || idxPath < 0 || idxRecorded > idxPath {
		t.Errorf("the recorded path must be tried before PATH:\n%s", hook)
	}

	// PATH is the fallback.
	if !strings.Contains(hook, "exec githints hook-run") {
		t.Errorf("hook has no PATH fallback:\n%s", hook)
	}

	// Neither available must not fail a commit: post-commit's status is
	// ignored, but pre-commit's is not, and a missing binary is not a reason
	// to block someone's work.
	if !strings.Contains(hook, "exit 0") {
		t.Errorf("hook must exit 0 when the binary is missing:\n%s", hook)
	}
	if !strings.Contains(hook, "githints init") {
		t.Errorf("hook should say how to repair itself:\n%s", hook)
	}
}

// TestHookIsPosixSh guards the shell the hook is written in. Git for Windows
// runs hooks through its bundled sh, so anything bash-only would break there
// and nowhere else.
func TestHookIsPosixSh(t *testing.T) {
	hook := hookScriptFor("/usr/local/bin/githints", "hook-precommit")
	if !strings.HasPrefix(hook, "#!/bin/sh\n") {
		t.Errorf("hook must start with #!/bin/sh:\n%s", hook)
	}
	for _, bashism := range []string{"[[", "==", "function ", "$(<"} {
		if strings.Contains(hook, bashism) {
			t.Errorf("hook uses the bash-only %q:\n%s", bashism, hook)
		}
	}
}

// The HMAC chain cannot see rows deleted from its tail, and a same-user
// attacker can re-sign the whole table anyway. verify used to print a Merkle
// root and never compare it with the one anchored in refs/notes/githints, so
// this deletion verified clean.
func TestVerifyDetectsRowsDeletedAfterAnchoring(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := chdirTempRepo(t)
	// No hooks: the test binary must not be run as a git hook. cmdHookRun is
	// called directly instead.
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := cmdRecord([]string{"-file=" + f, "-summary=add " + f}); err != nil {
			t.Fatalf("cmdRecord: %v", err)
		}
	}
	runGit(t, dir, "add", "a.go", "b.go")
	runGit(t, dir, "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "first")
	if err := cmdHookRun(); err != nil {
		t.Fatalf("cmdHookRun: %v", err)
	}
	if err := cmdVerify(); err != nil {
		t.Fatalf("verify on an untouched log: %v", err)
	}

	st, err := store.Open(filepath.Join(dir, ".githints", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.AllChanges()
	if err != nil || len(rows) < 2 {
		t.Fatalf("AllChanges: %d rows, %v", len(rows), err)
	}
	last := rows[len(rows)-1].ID
	if err := st.WithTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM changes WHERE id = ?", last)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if err := cmdVerify(); err == nil {
		t.Fatal("verify passed after the newest anchored row was deleted")
	}
}
