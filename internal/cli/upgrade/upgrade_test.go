package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cjrdz/githints/internal/cli/initialize"
	"github.com/cjrdz/githints/internal/clitest"
	"github.com/cjrdz/githints/internal/managed"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A repository set up by an older githints: hooks naming a binary that moved,
// a managed block with old text, and a .gitignore whose block was lost.
func TestUpgradeRefreshesAStaleRepository(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	exe, err := managed.RunningBinary()
	if err != nil {
		t.Fatal(err)
	}
	postCommit := filepath.Join(dir, ".git", "hooks", "post-commit")
	agents := filepath.Join(dir, "AGENTS.md")
	gitignore := filepath.Join(dir, ".gitignore")

	staleHook := managed.HookScriptFor("/opt/old/githints", "hook-run")
	write(t, postCommit, staleHook)
	staleAgents := strings.Replace(read(t, agents), "## Change tracking with githints", "## Change tracking (old text)", 1)
	write(t, agents, staleAgents)
	write(t, gitignore, "node_modules/\n")

	out, err := clitest.CaptureStdout(t, func() error { return Run([]string{"-dry-run"}, "9.9.9") })
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	for _, want := range []string{"would repoint the post-commit hook", "would refresh the githints block in AGENTS.md", "would refresh the githints block in .gitignore"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	if read(t, postCommit) != staleHook || read(t, agents) != staleAgents || read(t, gitignore) != "node_modules/\n" {
		t.Fatal("-dry-run changed files")
	}
	if strings.Contains(out, "restart your MCP servers") {
		t.Errorf("dry run asked for a restart:\n%s", out)
	}

	out, err = clitest.CaptureStdout(t, func() error { return Run(nil, "9.9.9") })
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	if got := read(t, postCommit); got != managed.HookScriptFor(exe, "hook-run") {
		t.Errorf("post-commit not repointed:\n%s", got)
	}
	if strings.Contains(read(t, agents), "(old text)") {
		t.Error("AGENTS.md block not refreshed")
	}
	if got := read(t, gitignore); !strings.HasPrefix(got, "node_modules/\n") || !strings.Contains(got, ".githints/") {
		t.Errorf(".gitignore: %q", got)
	}
	if !strings.Contains(out, "restart your MCP servers") || !strings.Contains(out, "already current:") || !strings.Contains(out, "pre-commit hook") {
		t.Errorf("upgrade output:\n%s", out)
	}

	out, err = clitest.CaptureStdout(t, func() error { return Run(nil, "9.9.9") })
	if err != nil {
		t.Fatalf("second upgrade: %v\n%s", err, out)
	}
	if !strings.Contains(out, "everything githints manages here is current") || strings.Contains(out, "restart your MCP servers") {
		t.Errorf("a second run should change nothing:\n%s", out)
	}
}

// Hand-written instructions and someone else's hook are not githints' to
// rewrite.
func TestUpgradeLeavesForeignFilesAlone(t *testing.T) {
	t.Setenv("GITHINTS_SALT_DIR", t.TempDir())
	dir := clitest.ChdirTempRepo(t)
	if err := initialize.Init(nil); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(dir, "AGENTS.md")
	preCommit := filepath.Join(dir, ".git", "hooks", "pre-commit")
	write(t, agents, "# Our own rules\n")
	write(t, preCommit, "#!/bin/sh\nexit 0\n")

	out, err := clitest.CaptureStdout(t, func() error { return Run(nil, "9.9.9") })
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	if read(t, agents) != "# Our own rules\n" || read(t, preCommit) != "#!/bin/sh\nexit 0\n" {
		t.Error("upgrade rewrote a file githints does not manage")
	}
	for _, want := range []string{"AGENTS.md has no githints block; left alone", "pre-commit hook was not installed by githints; left alone"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
