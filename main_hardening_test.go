package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
